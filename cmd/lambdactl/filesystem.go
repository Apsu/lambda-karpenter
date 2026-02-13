package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
)

// FilesystemCmd is the parent command for filesystem management.
type FilesystemCmd struct {
	List   FilesystemListCmd   `cmd:"" help:"List filesystems."`
	Create FilesystemCreateCmd `cmd:"" help:"Create a filesystem."`
	Delete FilesystemDeleteCmd `cmd:"" help:"Delete a filesystem."`
}

type FilesystemListCmd struct {
	APIFlags
}

func (c *FilesystemListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListFilesystems(ctx)
	fatalIf(err)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tREGION\tMOUNT\tIN USE\tSIZE")
	for _, fs := range items {
		size := formatBytes(fs.BytesUsed)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\n",
			fs.ID, fs.Name, fs.Region.Name, fs.MountPoint, fs.IsInUse, size)
	}
	w.Flush()
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
	fmt.Printf("id:     %s\n", fs.ID)
	fmt.Printf("name:   %s\n", fs.Name)
	fmt.Printf("region: %s\n", fs.Region.Name)
	fmt.Printf("mount:  %s\n", fs.MountPoint)
	return nil
}

type FilesystemDeleteCmd struct {
	APIFlags
	ID      string `name:"id" required:"" help:"Filesystem ID."`
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
