package main

import (
	"context"
	"strings"
	"time"

	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// ImageCmd is the parent command for image management.
type ImageCmd struct {
	List ImageListCmd `cmd:"" help:"List available images."`
	Get  ImageGetCmd  `cmd:"" help:"Get image details."`
}

type ImageListCmd struct {
	APIFlags
	Region string `name:"region" help:"Filter by region name."`
	Family string `name:"family" help:"Filter by image family."`
	Arch   string `name:"arch" help:"Filter by architecture."`
}

func (c *ImageListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)
	var rows [][]string
	for _, img := range items {
		if c.Region != "" && !strings.EqualFold(img.Region.Name, c.Region) {
			continue
		}
		if c.Family != "" && !strings.EqualFold(img.Family, c.Family) {
			continue
		}
		if c.Arch != "" && !strings.EqualFold(img.Arch, c.Arch) {
			continue
		}
		rows = append(rows, []string{img.ID, img.Family, img.Name, img.Region.Name, img.Arch})
	}
	printListTable([]string{"ID", "FAMILY", "NAME", "REGION", "ARCH"}, rows)
	return nil
}

type ImageGetCmd struct {
	APIFlags
	ID     string `name:"id" help:"Image ID."`
	Region string `name:"region" help:"Region name."`
	Family string `name:"family" help:"Image family."`
	Name   string `name:"name" help:"Image name."`
	Arch   string `name:"arch" help:"Architecture filter."`
	Latest bool   `name:"latest" help:"Return only the latest matching image."`
}

func (c *ImageGetCmd) Run() error {
	if c.ID == "" && c.Family == "" && c.Name == "" {
		fatalf("one of --id, --family, or --name is required")
	}

	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)

	var matches []lambdaclient.Image
	for _, img := range items {
		if c.ID != "" && img.ID != c.ID {
			continue
		}
		if c.Family != "" && img.Family != c.Family {
			continue
		}
		if c.Name != "" && img.Name != c.Name {
			continue
		}
		if c.Region != "" && img.Region.Name != c.Region {
			continue
		}
		if c.Arch != "" && img.Arch != c.Arch {
			continue
		}
		matches = append(matches, img)
	}

	if c.Latest && len(matches) > 0 {
		latestImg := matches[0]
		for _, img := range matches[1:] {
			if img.UpdatedTime.After(latestImg.UpdatedTime) {
				latestImg = img
			}
		}
		matches = []lambdaclient.Image{latestImg}
	}

	var rows [][]string
	for _, img := range matches {
		rows = append(rows, []string{
			img.ID, img.Family, img.Name, img.Region.Name, img.Arch,
			img.UpdatedTime.Format(time.RFC3339),
		})
	}
	printListTable([]string{"ID", "FAMILY", "NAME", "REGION", "ARCH", "UPDATED"}, rows)
	return nil
}
