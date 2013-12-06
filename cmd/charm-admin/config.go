// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package main

import (
	"fmt"

	"launchpad.net/gnuflag"

	"launchpad.net/juju-core/cmd"
	"launchpad.net/juju-core/store"
)

// ConfigCommand defines a command which requires a YAML config file.
type ConfigCommand struct {
	cmd.CommandBase
	ConfigPath string
	Config     *store.Config
}

type CharmdConfig struct {
	MongoUrl string `yaml:"mongo-url"`
}

func (c *ConfigCommand) Init(ctx *cmd.Context) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("--config is required")
	}
	return nil
}

func (c *ConfigCommand) Run(ctx *cmd.Context) (err error) {
	c.Config, err = store.ReadConfig(c.ConfigPath)
	return err
}

func (c *ConfigCommand) SetFlags(f *gnuflag.FlagSet) {
	f.StringVar(&c.ConfigPath, "config", "", "charmd configuration file")
}
