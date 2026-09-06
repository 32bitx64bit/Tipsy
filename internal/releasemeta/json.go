package releasemeta

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func checkJSONDepth(data []byte, maximum int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return wrap(ReasonMetadataMalformed, err, "invalid JSON")
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
				if depth > maximum {
					return fail(ReasonMetadataTooLarge, "JSON depth exceeds %d", maximum)
				}
			case '}', ']':
				depth--
			}
		}
	}
}
