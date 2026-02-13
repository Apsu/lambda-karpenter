package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
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

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPUBLIC KEY")
	for _, k := range keys {
		pub := k.PublicKey
		if len(pub) > 60 {
			pub = pub[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", k.ID, k.Name, pub)
	}
	w.Flush()
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

	fmt.Printf("id:         %s\n", key.ID)
	fmt.Printf("name:       %s\n", key.Name)
	fmt.Printf("public_key: %s\n", key.PublicKey)
	if key.PrivateKey != "" {
		fmt.Printf("\nprivate_key:\n%s\n", key.PrivateKey)
		fmt.Fprintln(os.Stderr, "WARNING: Save this private key now — Lambda does not store it.")
	}
	return nil
}

type SSHKeyDeleteCmd struct {
	APIFlags
	ID      string `name:"id" required:"" help:"SSH key ID."`
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
