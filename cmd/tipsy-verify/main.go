// tipsy-verify authenticates a release bundle without executing any target.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/releasemeta"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tipsy-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rootPath := flags.String("root", "", "externally authenticated TUF root.json")
	metadataDir := flags.String("metadata", "", "directory containing repository metadata")
	targetsDir := flags.String("targets", "", "directory containing TUF target evidence")
	targetPath := flags.String("target", "", "exact TUF artifact target path")
	artifactPath := flags.String("artifact", "", "artifact file to verify without execution")
	channel := flags.String("channel", "stable", "release channel: stable or beta")
	stateDir := flags.String("state-dir", "", "owner-private rollback state directory")
	jsonOutput := flags.Bool("json", false, "emit a stable JSON result")
	showVersion := flags.Bool("version", false, "print verifier version")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "tipsy-verify %s\n", releasemeta.VerifierVersion)
		return 0
	}
	result := releasemeta.Result{State: releasemeta.DevelopmentUnrestricted, Reason: releasemeta.ReasonBootstrapMissing}
	if *rootPath == "" {
		result, err := releasemeta.VerifyLocal(releasemeta.VerifyOptions{})
		result.Reason = releasemeta.CodeOf(err)
		emit(stdout, stderr, result, err, *jsonOutput)
		return 1
	}
	if *metadataDir == "" || *targetsDir == "" || *targetPath == "" || *artifactPath == "" {
		flags.Usage()
		return 2
	}
	root, err := releasemeta.ReadInitialRoot(*rootPath)
	if err != nil {
		emit(stdout, stderr, result, err, *jsonOutput)
		return 1
	}
	if *stateDir == "" {
		*stateDir, err = releasemeta.DefaultStateDir()
		if err != nil {
			emit(stdout, stderr, result, err, *jsonOutput)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(*stateDir), 0700); err != nil {
			emit(stdout, stderr, result, err, *jsonOutput)
			return 1
		}
	}
	result, err = releasemeta.VerifyLocal(releasemeta.VerifyOptions{
		InitialRoot: root, MetadataDir: *metadataDir, TargetsDir: *targetsDir,
		TargetPath: *targetPath, ArtifactPath: *artifactPath, Channel: *channel, StateDir: *stateDir,
	})
	if err != nil {
		result.Reason = releasemeta.CodeOf(err)
		emit(stdout, stderr, result, err, *jsonOutput)
		return 1
	}
	emit(stdout, stderr, result, nil, *jsonOutput)
	return 0
}

func emit(stdout, stderr io.Writer, result releasemeta.Result, err error, asJSON bool) {
	if asJSON {
		payload := struct {
			Result releasemeta.Result `json:"result"`
			Error  string             `json:"error,omitempty"`
		}{Result: result}
		if err != nil {
			payload.Error = releasemeta.DescribeError(err)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(true)
		_ = encoder.Encode(payload)
		return
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", result.State, releasemeta.DescribeError(err))
		return
	}
	fmt.Fprintln(stdout, result.Summary())
}
