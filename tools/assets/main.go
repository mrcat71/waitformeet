// Command assets bundles the TypeScript sources in web/src into the directory the
// server embeds at build time.
//
// esbuild is itself written in Go, so this needs no Node.js and no npm. It does not
// type-check: esbuild strips types without looking at them. CI runs `tsc --noEmit`
// separately for that, which is the only place Node appears in this repository.
//
// Run it with: go run ./tools/assets
package main

import (
	"fmt"
	"os"

	"github.com/evanw/esbuild/pkg/api"
)

const (
	srcDir = "web/src"
	outDir = "internal/web/static/dist"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "assets:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", outDir, err)
	}

	builds := []struct {
		name  string
		entry string
		out   string
		// format matters: the page script is a module, but the service worker is
		// loaded as a classic script so it works in every browser that supports
		// service workers at all.
		format api.Format
	}{
		{name: "page script", entry: srcDir + "/app.ts", out: outDir + "/app.js", format: api.FormatESModule},
		{name: "service worker", entry: srcDir + "/sw.ts", out: outDir + "/sw.js", format: api.FormatIIFE},
	}

	for _, b := range builds {
		result := api.Build(api.BuildOptions{
			EntryPoints:       []string{b.entry},
			Outfile:           b.out,
			Bundle:            true,
			Write:             true,
			Format:            b.format,
			Target:            api.ES2022,
			Platform:          api.PlatformBrowser,
			MinifyWhitespace:  true,
			MinifyIdentifiers: true,
			MinifySyntax:      true,
			Charset:           api.CharsetUTF8,
			LogLevel:          api.LogLevelWarning,
			// Deterministic output matters: CI rebuilds and fails if the committed
			// bundle differs, so the build must not embed paths or timestamps.
			AbsWorkingDir: mustWorkingDir(),
		})

		if len(result.Errors) > 0 {
			for _, e := range result.Errors {
				loc := ""
				if e.Location != nil {
					loc = fmt.Sprintf("%s:%d:%d: ", e.Location.File, e.Location.Line, e.Location.Column)
				}
				fmt.Fprintf(os.Stderr, "assets: %s: %s%s\n", b.name, loc, e.Text)
			}
			return fmt.Errorf("%s failed with %d error(s)", b.name, len(result.Errors))
		}
		fmt.Printf("assets: built %s -> %s\n", b.entry, b.out)
	}

	return nil
}

func mustWorkingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		panic("assets: cannot determine working directory: " + err.Error())
	}
	return wd
}
