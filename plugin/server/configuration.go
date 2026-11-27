package main

import (
	"fmt"
	"net/url"
	"strings"
)

// configuration is the plugin's settings from the System Console (see the
// settings_schema in plugin.json). It's replaced, never modified, when the
// settings change.
type configuration struct {
	// BotURL is where the attendance bot listens on the internal Docker
	// network, e.g. http://attendance:8080.
	BotURL string
	// Secret is shared with the bot (PLUGIN_SECRET in its .env).
	Secret string
}

// validate reports what's missing or wrong, for the log and the health check.
func (c *configuration) validate() error {
	u, err := url.Parse(strings.TrimSpace(c.BotURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("Bot URL %q must be like http://attendance:8080", c.BotURL)
	}
	if len(strings.TrimSpace(c.Secret)) < 32 {
		return fmt.Errorf("the shared secret is missing or shorter than 32 characters")
	}
	return nil
}

func (p *Plugin) getConfiguration() *configuration {
	p.configurationLock.RLock()
	defer p.configurationLock.RUnlock()
	if p.configuration == nil {
		return &configuration{}
	}
	return p.configuration
}

// OnConfigurationChange loads the settings whenever an admin saves them.
func (p *Plugin) OnConfigurationChange() error {
	c := new(configuration)
	if err := p.API.LoadPluginConfiguration(c); err != nil {
		return fmt.Errorf("load plugin configuration: %w", err)
	}
	p.configurationLock.Lock()
	p.configuration = c
	p.configurationLock.Unlock()
	if err := c.validate(); err != nil {
		p.API.LogWarn("Attendance plugin isn't connected to the bot yet", "reason", err.Error())
	}
	return nil
}
