package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"rillway/internal/config"
	"rillway/internal/dockerproxy"
)

func dockerCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "export" {
		return errors.New("docker requires export")
	}
	fs, path := flags(ctx, "docker export", out)
	target := fs.String("target", "client", cliText(ctx, "export format: daemon, client, env or compose"))
	proxyURL := fs.String("proxy-url", "", cliText(ctx, "Docker-reachable Rillway HTTP Proxy URL"))
	noProxy := fs.String("no-proxy", "", cliText(ctx, "comma-separated Docker bypass domains, IPs or CIDRs"))
	input := fs.String("input", "", cliText(ctx, "existing Docker JSON to merge; source file is not modified"))
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("docker requires export")
	}
	var explicitConfig, explicitBypass bool
	fs.Visit(func(f *flag.Flag) {
		explicitConfig = explicitConfig || f.Name == "config"
		explicitBypass = explicitBypass || f.Name == "no-proxy"
	})
	c := config.Default("")
	if *proxyURL == "" || explicitConfig {
		var err error
		c, err = config.Load(*path)
		if err != nil {
			return err
		}
	}
	var settings dockerproxy.Settings
	var err error
	if *proxyURL != "" {
		settings, err = dockerproxy.New(*proxyURL, dockerproxy.DefaultBypass(c.PAC))
	} else {
		settings, err = dockerproxy.FromConfig(c)
	}
	if err != nil {
		return err
	}
	if explicitBypass {
		settings, err = dockerproxy.New(settings.ProxyURL, *noProxy)
		if err != nil {
			return err
		}
	}
	var previous []byte
	if *input != "" {
		file, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		previous, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return err
		}
	}
	data, err := settings.Export(*target, previous)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}
