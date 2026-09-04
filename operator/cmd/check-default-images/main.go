/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/imagecheck"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run() error {
	valuesPath := flag.String("values", "", "path to the Helm values.yaml file")
	requestTimeout := flag.Duration("timeout", 30*time.Second, "timeout for each registry check")
	flag.Parse()

	if *valuesPath == "" {
		return errors.New("--values is required")
	}
	if *requestTimeout <= 0 {
		return errors.New("--timeout must be greater than zero")
	}

	valuesYAML, err := os.ReadFile(*valuesPath)
	if err != nil {
		return fmt.Errorf("reading values file: %w", err)
	}
	images, err := imagecheck.ParseDefaultImages(valuesYAML)
	if err != nil {
		return err
	}

	checker := imagecheck.NewChecker(nil)
	failures := 0
	for _, image := range images {
		ctx, cancel := context.WithTimeout(context.Background(), *requestTimeout)
		err := checker.Check(ctx, image.Reference)
		cancel()
		if err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "FAIL %s (%s): %v\n", image.Path, image.Reference, err)
			continue
		}
		fmt.Printf("OK   %s (%s)\n", image.Path, image.Reference)
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d default images failed availability checks", failures, len(images))
	}
	return nil
}
