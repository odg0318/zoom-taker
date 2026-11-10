// Command zoom-taker is the single Go binary replacing the Python pipeline:
//
//	zoom-taker                       serve (default)
//	zoom-taker serve                 web server + recorder page
//	zoom-taker meeting <name>        full meeting pipeline (filters + merge)
//	zoom-taker transcribe <file>     single-file transcription
//	zoom-taker download [model]      download the GGML model into the cache
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	app "github.com/odg0318/zoom-taker"

	"github.com/odg0318/zoom-taker/internal/config"
	"github.com/odg0318/zoom-taker/internal/modelcache"
	"github.com/odg0318/zoom-taker/internal/pipeline"
	"github.com/odg0318/zoom-taker/internal/server"
	"github.com/odg0318/zoom-taker/internal/stt"
)

func main() {
	cfg := config.Load()
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = cmdServe(cfg)
	case "meeting":
		err = cmdMeeting(cfg, args)
	case "transcribe":
		err = cmdTranscribe(cfg, args)
	case "download":
		err = cmdDownload(cfg, args)
	default:
		fmt.Fprintf(os.Stderr, "usage: zoom-taker [serve|meeting|transcribe|download] [args]\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// engineFactory resolves the model and loads the STT engine once per
// process; the server reuses it across jobs (Python loaded a fresh
// subprocess per job).
func engineFactory(cfg config.Config, log func(string)) func() (stt.Engine, error) {
	var (
		once sync.Once
		eng  stt.Engine
		err  error
	)
	return func() (stt.Engine, error) {
		once.Do(func() {
			var path string
			path, err = modelcache.Resolve(context.Background(), cfg.Model,
				cfg.ModelCacheDir, cfg.HFEndpoint, cfg.HFToken, log)
			if err != nil {
				return
			}
			eng, err = stt.NewEngine(path, log)
		})
		return eng, err
	}
}

func cmdServe(cfg config.Config) error {
	return server.Run(cfg, app.HTML, engineFactory(cfg, func(s string) { fmt.Println(s) }))
}

func cmdMeeting(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("meeting", flag.ExitOnError)
	language := fs.String("language", cfg.Language, "meeting language: kr, en, cn, or a combo like kr+en")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: zoom-taker meeting <name> [--language kr|en|cn|kr+en]")
	}
	newEngine := engineFactory(cfg, func(s string) { fmt.Println(s) })
	eng, err := newEngine()
	if err != nil {
		return err
	}
	_, err = pipeline.Meeting(context.Background(), eng, fs.Arg(0), pipeline.Options{
		RecordingsDir: cfg.RecordingsDir,
		OutputDir:     cfg.OutputDir,
		Language:      *language,
		Log:           func(s string) { fmt.Println(s) },
	})
	return err
}

func cmdTranscribe(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	language := fs.String("language", cfg.Language, "meeting language: kr, en, cn, or a combo like kr+en")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: zoom-taker transcribe <file> [--language kr|en|cn|kr+en]")
	}
	newEngine := engineFactory(cfg, func(s string) { fmt.Println(s) })
	eng, err := newEngine()
	if err != nil {
		return err
	}
	_, err = pipeline.TranscribeFile(context.Background(), eng, fs.Arg(0), *language,
		cfg.RecordingsDir, cfg.OutputDir, func(s string) { fmt.Println(s) })
	return err
}

func cmdDownload(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec := cfg.Model
	if fs.NArg() == 1 {
		spec = fs.Arg(0)
	}
	_, err := modelcache.Resolve(context.Background(), spec,
		cfg.ModelCacheDir, cfg.HFEndpoint, cfg.HFToken, func(s string) { fmt.Println(s) })
	return err
}
