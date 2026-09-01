package client

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// PluginList returns the list of loaded plugins.
func (c *Client) PluginList(ctx context.Context) (*daemon.PluginListResult, error) {
	var result daemon.PluginListResult
	if err := c.Call(ctx, daemon.MethodPluginList, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PluginEnable enables a plugin by name.
func (c *Client) PluginEnable(ctx context.Context, name string) error {
	return c.Call(ctx, daemon.MethodPluginEnable, daemon.PluginEnableParams{Name: name}, nil)
}

// PluginDisable disables a plugin by name.
func (c *Client) PluginDisable(ctx context.Context, name string) error {
	return c.Call(ctx, daemon.MethodPluginDisable, daemon.PluginDisableParams{Name: name}, nil)
}

// PluginReload reloads the plugin root and returns per-plugin results.
func (c *Client) PluginReload(ctx context.Context) (*daemon.PluginReloadResult, error) {
	var result daemon.PluginReloadResult
	if err := c.Call(ctx, daemon.MethodPluginReload, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SkillList returns the list of loaded skills.
func (c *Client) SkillList(ctx context.Context) (*daemon.SkillListResult, error) {
	var result daemon.SkillListResult
	if err := c.Call(ctx, daemon.MethodSkillList, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SkillEnable enables a skill by name.
func (c *Client) SkillEnable(ctx context.Context, name string) error {
	return c.Call(ctx, daemon.MethodSkillEnable, daemon.SkillEnableParams{Name: name}, nil)
}

// SkillDisable disables a skill by name.
func (c *Client) SkillDisable(ctx context.Context, name string) error {
	return c.Call(ctx, daemon.MethodSkillDisable, daemon.SkillDisableParams{Name: name}, nil)
}

// SkillReload reloads the skill root and returns per-skill results.
func (c *Client) SkillReload(ctx context.Context) (*daemon.SkillReloadResult, error) {
	var result daemon.SkillReloadResult
	if err := c.Call(ctx, daemon.MethodSkillReload, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
