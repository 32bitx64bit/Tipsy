package apk

import (
	"archive/zip"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const apkSigBlockMagic = "APK Sig Block 42"

const (
	apkSigIDV2  = 0x7109871a
	apkSigIDV3  = 0xf05368c0
	apkSigIDV31 = 0x1b93ad61
)

var (
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
)

func parseSigning(ra io.ReaderAt, size int64, zr *zip.Reader) SigningInfo {
	var info SigningInfo
	info.HasV1 = detectV1(zr)
	v1Certs, v1Err := parseV1Certs(zr)

	block, ids, blockErr := readAPKSigningBlock(ra, size)
	if blockErr != nil {
		// No signing block is normal for unsigned / v1-only APKs.
		apkLog().Debug("apk signing block", "err", blockErr)
	}
	for _, id := range ids {
		switch id {
		case apkSigIDV2:
			info.HasV2 = true
		case apkSigIDV3:
			info.HasV3 = true
		case apkSigIDV31:
			info.HasV3_1 = true
		}
	}

	var schemeCerts []*x509.Certificate
	var schemeErr error
	if len(block) > 0 {
		schemeCerts, schemeErr = certsFromSigningBlock(block)
	}

	certs := append([]*x509.Certificate{}, v1Certs...)
	certs = append(certs, schemeCerts...)
	seen := map[string]struct{}{}
	for _, c := range certs {
		if c == nil {
			continue
		}
		h := sha256Hex(c.Raw)
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		info.CertSHA256 = append(info.CertSHA256, h)
		info.Subjects = append(info.Subjects, c.Subject.String())
	}

	var errs []string
	if v1Err != nil && info.HasV1 {
		errs = append(errs, v1Err.Error())
	}
	if schemeErr != nil && (info.HasV2 || info.HasV3 || info.HasV3_1) {
		errs = append(errs, schemeErr.Error())
	}
	if blockErr != nil && len(ids) > 0 {
		errs = append(errs, blockErr.Error())
	}
	if len(errs) > 0 {
		info.ParseError = strings.Join(errs, "; ")
	}
	return info
}

func detectV1(zr *zip.Reader) bool {
	for _, f := range zr.File {
		if isV1SignatureFile(f.Name) {
			return true
		}
	}
	return false
}

func isV1SignatureFile(name string) bool {
	name = filepath.ToSlash(name)
	if !strings.HasPrefix(strings.ToUpper(name), "META-INF/") {
		return false
	}
	upper := strings.ToUpper(name)
	return strings.HasSuffix(upper, ".RSA") ||
		strings.HasSuffix(upper, ".DSA") ||
		strings.HasSuffix(upper, ".EC")
}

func parseV1Certs(zr *zip.Reader) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	var firstErr error
	for _, f := range zr.File {
		if !isV1SignatureFile(f.Name) {
			continue
		}
		data, err := readZipEntry(f)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cs, err := certsFromPKCS7(data)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.Name, err)
			}
			continue
		}
		certs = append(certs, cs...)
	}
	return certs, firstErr
}

func certsFromPKCS7(der []byte) ([]*x509.Certificate, error) {
	if c, err := x509.ParseCertificate(der); err == nil {
		return []*x509.Certificate{c}, nil
	}
	var ci struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue `asn1:"tag:0,explicit,optional"`
	}
	if _, err := asn1.Unmarshal(der, &ci); err != nil {
		if found := scanCertificates(der); len(found) > 0 {
			return found, nil
		}
		return nil, fmt.Errorf("pkcs7: %w", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		// Some producers store a raw certificate.
		if c, err := x509.ParseCertificate(der); err == nil {
			return []*x509.Certificate{c}, nil
		}
		return nil, fmt.Errorf("pkcs7: not signedData")
	}
	var sd struct {
		Version          int
		DigestAlgorithms asn1.RawValue
		EncapContentInfo asn1.RawValue
		Certificates     []asn1.RawValue `asn1:"optional,tag:0,set"`
		CRLs             asn1.RawValue   `asn1:"optional,tag:1"`
		SignerInfos      asn1.RawValue
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		if _, err2 := asn1.Unmarshal(ci.Content.FullBytes, &sd); err2 != nil {
			if found := scanCertificates(der); len(found) > 0 {
				return found, nil
			}
			return nil, fmt.Errorf("pkcs7 signedData: %w", err)
		}
	}
	var certs []*x509.Certificate
	for _, raw := range sd.Certificates {
		c, err := x509.ParseCertificate(raw.FullBytes)
		if err != nil {
			c, err = x509.ParseCertificate(raw.Bytes)
		}
		if err != nil {
			continue
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		certs = scanCertificates(der)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("pkcs7: no certificates")
	}
	return certs, nil
}

func scanCertificates(der []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	for i := 0; i < len(der); {
		if der[i] != 0x30 {
			i++
			continue
		}
		var raw asn1.RawValue
		rest, err := asn1.Unmarshal(der[i:], &raw)
		if err != nil {
			i++
			continue
		}
		consumed := len(der[i:]) - len(rest)
		if consumed <= 0 {
			i++
			continue
		}
		c, err := x509.ParseCertificate(der[i : i+consumed])
		if err == nil {
			certs = append(certs, c)
			i += consumed
			continue
		}
		i++
	}
	return certs
}

func encodePKCS7(certDER []byte) ([]byte, error) {
	emptySET := asn1.RawValue{Class: asn1.ClassUniversal, Tag: 17, IsCompound: true}
	type encap struct {
		OID asn1.ObjectIdentifier
	}
	type signedData struct {
		Version          int
		DigestAlgorithms asn1.RawValue
		EncapContentInfo encap
		Certificates     []asn1.RawValue `asn1:"optional,tag:0,set"`
		SignerInfos      asn1.RawValue
	}
	type envelope struct {
		ContentType asn1.ObjectIdentifier
		Content     signedData `asn1:"explicit,tag:0"`
	}
	return asn1.Marshal(envelope{
		ContentType: oidSignedData,
		Content: signedData{
			Version:          1,
			DigestAlgorithms: emptySET,
			EncapContentInfo: encap{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}},
			Certificates:     []asn1.RawValue{{FullBytes: certDER}},
			SignerInfos:      emptySET,
		},
	})
}

func v2SignerBlock(certDER []byte) []byte {
	certs := u32pref(u32pref(certDER))
	digests := u32pref(nil)
	signedData := append(digests, certs...)
	signer := u32pref(signedData)
	return u32pref(signer)
}

func u32pref(b []byte) []byte {
	out := appendU32(nil, uint32(len(b)))
	return append(out, b...)
}

type sigPair struct {
	id    uint32
	value []byte
}

func readAPKSigningBlock(ra io.ReaderAt, size int64) ([]sigPair, []uint32, error) {
	cdOff, err := findCentralDirectoryOffset(ra, size)
	if err != nil {
		return nil, nil, err
	}
	if cdOff < 32 {
		return nil, nil, fmt.Errorf("signing: no room for signing block")
	}
	// magic (16) + size (8) immediately precede the central directory.
	tail := make([]byte, 24)
	if _, err := ra.ReadAt(tail, cdOff-24); err != nil {
		return nil, nil, err
	}
	if string(tail[8:]) != apkSigBlockMagic {
		return nil, nil, fmt.Errorf("signing: magic not found")
	}
	blockSize := binary.LittleEndian.Uint64(tail[:8])
	// blockSize is the size of the block excluding the leading uint64, so the
	// file range is [cdOff - 8 - blockSize, cdOff).
	if blockSize < 24 || blockSize > uint64(cdOff) {
		return nil, nil, fmt.Errorf("signing: implausible block size %d", blockSize)
	}
	start := cdOff - int64(blockSize) - 8
	if start < 0 {
		return nil, nil, fmt.Errorf("signing: block start negative")
	}
	buf := make([]byte, int(blockSize)+8)
	if _, err := ra.ReadAt(buf, start); err != nil {
		return nil, nil, err
	}
	headSize := binary.LittleEndian.Uint64(buf[:8])
	if headSize != blockSize {
		return nil, nil, fmt.Errorf("signing: size mismatch")
	}
	if string(buf[len(buf)-16:]) != apkSigBlockMagic {
		return nil, nil, fmt.Errorf("signing: trailing magic mismatch")
	}
	// pairs sit between the leading size and the trailing size+magic (24 bytes).
	return parseSigningPairs(buf[8 : len(buf)-24])
}

func parseSigningPairs(pairs []byte) ([]sigPair, []uint32, error) {
	var out []sigPair
	var ids []uint32
	off := 0
	for off+12 <= len(pairs) {
		pairSize := binary.LittleEndian.Uint64(pairs[off:])
		off += 8
		remain := len(pairs) - off
		if remain < 0 || pairSize < 4 || pairSize > uint64(remain) {
			return out, ids, fmt.Errorf("signing: truncated ID-value pair")
		}
		id := binary.LittleEndian.Uint32(pairs[off:])
		val := pairs[off+4 : off+int(pairSize)]
		out = append(out, sigPair{id: id, value: append([]byte(nil), val...)})
		ids = append(ids, id)
		off += int(pairSize)
	}
	return out, ids, nil
}

func certsFromSigningBlock(pairs []sigPair) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	var firstErr error
	for _, p := range pairs {
		switch p.id {
		case apkSigIDV2, apkSigIDV3, apkSigIDV31:
			cs, err := certsFromSignerBlock(p.value)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			certs = append(certs, cs...)
		}
	}
	if len(certs) == 0 {
		return nil, firstErr
	}
	return certs, firstErr
}

func certsFromSignerBlock(value []byte) ([]*x509.Certificate, error) {
	signers, _, err := readU32Prefixed(value, 0)
	if err != nil {
		return nil, fmt.Errorf("signing: signers: %w", err)
	}
	var certs []*x509.Certificate
	off := 0
	for off < len(signers) {
		signer, next, err := readU32Prefixed(signers, off)
		if err != nil {
			break
		}
		off = next
		signedData, _, err := readU32Prefixed(signer, 0)
		if err != nil {
			continue
		}
		// signed data: digests then certificates (v2 and v3 share this prefix).
		_, afterDigests, err := readU32Prefixed(signedData, 0)
		if err != nil {
			continue
		}
		certsBlock, _, err := readU32Prefixed(signedData, afterDigests)
		if err != nil {
			continue
		}
		cOff := 0
		for cOff < len(certsBlock) {
			der, n, err := readU32Prefixed(certsBlock, cOff)
			if err != nil {
				break
			}
			cOff = n
			c, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}
			certs = append(certs, c)
		}
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("signing: no parseable certificates in v2/v3 block")
	}
	return certs, nil
}

func readU32Prefixed(b []byte, off int) (payload []byte, next int, err error) {
	if off < 0 || off+4 > len(b) {
		return nil, 0, fmt.Errorf("truncated length prefix")
	}
	n := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if n < 0 || off+n > len(b) {
		return nil, 0, fmt.Errorf("truncated length-prefixed blob")
	}
	return b[off : off+n], off + n, nil
}

func findCentralDirectoryOffset(ra io.ReaderAt, size int64) (int64, error) {
	if size < 22 {
		return 0, fmt.Errorf("signing: file too small for zip")
	}
	maxComment := int64(65535)
	readLen := 22 + maxComment
	if readLen > size {
		readLen = size
	}
	buf := make([]byte, readLen)
	off := size - readLen
	if _, err := ra.ReadAt(buf, off); err != nil && err != io.EOF {
		return 0, err
	}
	// Search backwards for EOCD signature 0x06054b50.
	for i := len(buf) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != 0x06054b50 {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+22+commentLen != len(buf) {
			continue
		}
		cdOff := binary.LittleEndian.Uint32(buf[i+16:])
		if cdOff == 0xffffffff {
			return findZIP64CDOffset(ra, off+int64(i))
		}
		return int64(cdOff), nil
	}
	return 0, fmt.Errorf("signing: EOCD not found")
}

func findZIP64CDOffset(ra io.ReaderAt, eocdOff int64) (int64, error) {
	if eocdOff < 20 {
		return 0, fmt.Errorf("signing: no zip64 locator")
	}
	loc := make([]byte, 20)
	if _, err := ra.ReadAt(loc, eocdOff-20); err != nil {
		return 0, err
	}
	if binary.LittleEndian.Uint32(loc) != 0x07064b50 {
		return 0, fmt.Errorf("signing: zip64 locator missing")
	}
	z64off := binary.LittleEndian.Uint64(loc[8:])
	hdr := make([]byte, 56)
	if _, err := ra.ReadAt(hdr, int64(z64off)); err != nil {
		return 0, err
	}
	if binary.LittleEndian.Uint32(hdr) != 0x06064b50 {
		return 0, fmt.Errorf("signing: zip64 eocd missing")
	}
	return int64(binary.LittleEndian.Uint64(hdr[48:])), nil
}

// insertAPKSigningBlock splices APK Signature Scheme ID-value pairs immediately
// before the ZIP central directory. Used by tests to exercise v2/v3 detection
// without apksigner.
func insertAPKSigningBlock(zipBytes []byte, pairs ...sigPair) ([]byte, error) {
	cdOff, err := findCentralDirectoryOffset(bytesReaderAt(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}
	var pairBytes []byte
	for _, p := range pairs {
		pair := appendU32(nil, p.id)
		pair = append(pair, p.value...)
		pairBytes = appendU64(pairBytes, uint64(len(pair)))
		pairBytes = append(pairBytes, pair...)
	}
	blockSize := uint64(len(pairBytes) + 8 + 16)
	var block []byte
	block = appendU64(block, blockSize)
	block = append(block, pairBytes...)
	block = appendU64(block, blockSize)
	block = append(block, []byte(apkSigBlockMagic)...)

	out := make([]byte, 0, len(zipBytes)+len(block))
	out = append(out, zipBytes[:cdOff]...)
	out = append(out, block...)
	out = append(out, zipBytes[cdOff:]...)

	// Patch EOCD CD offset (and ZIP64 if present). Files from archive/zip are small.
	newCD := uint64(cdOff) + uint64(len(block))
	eocdOff := int64(len(out) - 22)
	// Locate real EOCD in case of comment (none for archive/zip).
	// Search from the end the same way as findCentralDirectoryOffset, then patch.
	size := int64(len(out))
	maxComment := int64(65535)
	readLen := 22 + maxComment
	if readLen > size {
		readLen = size
	}
	bufStart := size - readLen
	buf := out[bufStart:]
	for i := len(buf) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != 0x06054b50 {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+22+commentLen != len(buf) {
			continue
		}
		eocdOff = bufStart + int64(i)
		break
	}
	if newCD > 0xffffffff {
		return nil, fmt.Errorf("signing: CD offset exceeds zip32")
	}
	binary.LittleEndian.PutUint32(out[eocdOff+16:], uint32(newCD))
	return out, nil
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
