package main

import (
	"context"
	"fmt"
	"os"
)

// SSHKeyCmd is the parent command for SSH key management.
type SSHKeyCmd struct {
	List   SSHKeyListCmd   `cmd:"" help:"List SSH keys."`
	Add    SSHKeyAddCmd    `cmd:"" help:"Add an SSH key."`
	Delete SSHKeyDeleteCmd `cmd:"" help:"Delete an SSH key."`
}

type SSHKeyListCmd struct {
	APIFlags
}

func (c *SSHKeyListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	keys, err := client.ListSSHKeys(ctx)
	fatalIf(err)

	var rows [][]string
	for _, k := range keys {
		pub := k.PublicKey
		if len(pub) > 60 {
			pub = pub[:57] + "..."
		}
		rows = append(rows, []string{k.ID, k.Name, pub})
	}
	printListTable([]string{"ID", "NAME", "PUBLIC KEY"}, rows)
	return nil
}

type SSHKeyAddCmd struct {
	APIFlags
	Name          string `name:"name" required:"" help:"Name for the SSH key."`
	PublicKey     string `name:"public-key" help:"Public key string (omit to generate a key pair)."`
	PublicKeyFile string `name:"public-key-file" help:"Path to public key file."`
}

func (c *SSHKeyAddCmd) Run() error {
	pubKey := c.PublicKey
	if pubKey == "" && c.PublicKeyFile != "" {
		data, err := os.ReadFile(c.PublicKeyFile)
		fatalIf(err)
		pubKey = string(data)
	}

	client := c.mustClient()
	ctx := context.Background()
	key, err := client.AddSSHKey(ctx, c.Name, pubKey)
	fatalIf(err)

	fields := [][]string{
		{"ID", key.ID},
		{"Name", key.Name},
		{"Public Key", key.PublicKey},
	}
	printDetailTable(fields)
	if key.PrivateKey != "" {
		fmt.Printf("\nprivate_key:\n%s\n", key.PrivateKey)
		fmt.Fprintln(os.Stderr, "WARNING: Save this private key now — Lambda does not store it.")
	}
	return nil
}

type SSHKeyDeleteCmd struct {
	APIFlags
	ID      string `arg:"" help:"SSH key ID."`
	Confirm bool   `name:"confirm" help:"Skip interactive confirmation."`
}

func (c *SSHKeyDeleteCmd) Run() error {
	if !c.Confirm {
		confirmAction("delete SSH key " + c.ID)
	}
	client := c.mustClient()
	ctx := context.Background()
	fatalIf(client.DeleteSSHKey(ctx, c.ID))
	fmt.Printf("deleted SSH key %s\n", c.ID)
	return nil
}
