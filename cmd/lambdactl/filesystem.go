package main

import (
	"context"
	"fmt"
	"strings"
)

// FilesystemCmd is the parent command for filesystem management.
type FilesystemCmd struct {
	List   FilesystemListCmd   `cmd:"" help:"List filesystems."`
	Create FilesystemCreateCmd `cmd:"" help:"Create a filesystem."`
	Delete FilesystemDeleteCmd `cmd:"" help:"Delete a filesystem."`
}

type FilesystemListCmd struct {
	APIFlags
	Region string `name:"region" help:"Filter by region name."`
}

func (c *FilesystemListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListFilesystems(ctx)
	fatalIf(err)

	var rows [][]string
	for _, fs := range items {
		if c.Region != "" && !strings.EqualFold(fs.Region.Name, c.Region) {
			continue
		}
		inUse := "false"
		if fs.IsInUse {
			inUse = "true"
		}
		rows = append(rows, []string{
			fs.ID, fs.Name, fs.Region.Name, fs.MountPoint, inUse, formatBytes(fs.BytesUsed),
		})
	}
	printListTable([]string{"ID", "NAME", "REGION", "MOUNT", "IN USE", "SIZE"}, rows)
	return nil
}

// formatBytes returns a human-readable byte size.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(b)/float64(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b > 0:
		return fmt.Sprintf("%d B", b)
	default:
		return "-"
	}
}

type FilesystemCreateCmd struct {
	APIFlags
	Name   string `name:"name" required:"" help:"Filesystem name."`
	Region string `name:"region" required:"" help:"Region."`
}

func (c *FilesystemCreateCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	fs, err := client.CreateFilesystem(ctx, c.Name, c.Region)
	fatalIf(err)
	printDetailTable([][]string{
		{"ID", fs.ID},
		{"Name", fs.Name},
		{"Region", fs.Region.Name},
		{"Mount", fs.MountPoint},
	})
	return nil
}

type FilesystemDeleteCmd struct {
	APIFlags
	ID      string `arg:"" help:"Filesystem ID."`
	Confirm bool   `name:"confirm" help:"Skip interactive confirmation."`
}

func (c *FilesystemDeleteCmd) Run() error {
	if !c.Confirm {
		confirmAction("delete filesystem " + c.ID)
	}
	client := c.mustClient()
	ctx := context.Background()
	fatalIf(client.DeleteFilesystem(ctx, c.ID))
	fmt.Printf("deleted filesystem %s\n", c.ID)
	return nil
}
