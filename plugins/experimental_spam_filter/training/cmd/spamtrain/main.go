package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"rolltop/plugins/experimental_spam_filter/training"
)

const (
	defaultCache    = ".cache/rolltop-spam-corpus"
	defaultModel    = "plugins/experimental_spam_filter/model/model.bin"
	defaultMetadata = "plugins/experimental_spam_filter/model/model.json"
	defaultReport   = "plugins/experimental_spam_filter/model/benchmark.json"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "spamtrain:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}
	switch arguments[0] {
	case "download":
		flags := flag.NewFlagSet("spamtrain download", flag.ContinueOnError)
		cache := flags.String("cache", defaultCache, "corpus archive/extraction cache")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return usageError()
		}
		return training.Download(context.Background(), *cache, os.Stdout)
	case "train":
		flags := flag.NewFlagSet("spamtrain train", flag.ContinueOnError)
		cache := flags.String("cache", defaultCache, "downloaded corpus cache")
		modelPath := flags.String("model", defaultModel, "output model binary")
		metadataPath := flags.String("metadata", defaultMetadata, "output model metadata")
		reportPath := flags.String("report", defaultReport, "output benchmark report")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return usageError()
		}
		return training.Train(*cache, training.ArtifactPaths{
			Model: *modelPath, Metadata: *metadataPath, Report: *reportPath,
		}, os.Stdout)
	case "verify":
		flags := flag.NewFlagSet("spamtrain verify", flag.ContinueOnError)
		modelPath := flags.String("model", defaultModel, "checked-in model binary")
		metadataPath := flags.String("metadata", defaultMetadata, "checked-in model metadata")
		reportPath := flags.String("report", defaultReport, "checked-in benchmark report")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return usageError()
		}
		return training.Verify(training.ArtifactPaths{
			Model: *modelPath, Metadata: *metadataPath, Report: *reportPath,
		}, os.Stdout)
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("usage: spamtrain <download|train|verify> [flags]")
}
