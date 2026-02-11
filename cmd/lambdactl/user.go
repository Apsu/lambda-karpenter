package main

import (
	"context"
	"fmt"
	"os"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const userNamespace = "kube-system"

// UserCmd is the parent for user management subcommands.
type UserCmd struct {
	Create  UserCreateCmd  `cmd:"" help:"Create per-user SA + token kubeconfig."`
	Rotate  UserRotateCmd  `cmd:"" help:"Rotate user token in existing kubeconfig."`
	Cleanup UserCleanupCmd `cmd:"" help:"Delete user SA + ClusterRoleBinding."`
}

type UserCreateCmd struct {
	Username   string        `name:"username" required:"" help:"Username."`
	Kubeconfig string        `name:"kubeconfig" required:"" help:"Path to admin kubeconfig."`
	Context    string        `name:"context" help:"Kubeconfig context to use."`
	Expiration time.Duration `name:"expiration" default:"8760h" help:"Token expiration duration."`
	Output     string        `name:"output" help:"Output kubeconfig path (default USERNAME.kubeconfig)."`
	Role       string        `name:"role" default:"cluster-admin" help:"ClusterRole to bind."`
}

func (c *UserCreateCmd) Run() error {
	if c.Output == "" {
		c.Output = c.Username + ".kubeconfig"
	}

	cs := mustClientset(c.Kubeconfig, c.Context)
	ctx := context.Background()

	// Create ServiceAccount.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.Username,
			Namespace: userNamespace,
		},
	}
	_, err := cs.CoreV1().ServiceAccounts(userNamespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		fatalIf(err)
	}
	fmt.Printf("serviceaccount/%s ensured in %s\n", c.Username, userNamespace)

	// Create ClusterRoleBinding.
	crbName := "lambdactl-" + c.Username
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      c.Username,
			Namespace: userNamespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     c.Role,
		},
	}
	_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		fatalIf(err)
	}
	fmt.Printf("clusterrolebinding/%s ensured\n", crbName)

	// Create token.
	expSecs := int64(c.Expiration.Seconds())
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expSecs,
		},
	}
	result, err := cs.CoreV1().ServiceAccounts(userNamespace).CreateToken(ctx, c.Username, tokenReq, metav1.CreateOptions{})
	fatalIf(err)
	token := result.Status.Token
	fmt.Printf("token created (expires in %s)\n", c.Expiration)

	// Read admin kubeconfig to extract cluster info.
	adminCfg, err := clientcmd.LoadFromFile(c.Kubeconfig)
	fatalIf(err)

	// Find the cluster to extract CA and server.
	var clusterName string
	var clusterInfo *clientcmdapi.Cluster
	if c.Context != "" {
		if ctxObj, ok := adminCfg.Contexts[c.Context]; ok {
			clusterName = ctxObj.Cluster
			clusterInfo = adminCfg.Clusters[clusterName]
		}
	}
	if clusterInfo == nil {
		if ctxObj, ok := adminCfg.Contexts[adminCfg.CurrentContext]; ok {
			clusterName = ctxObj.Cluster
			clusterInfo = adminCfg.Clusters[clusterName]
		}
	}
	if clusterInfo == nil {
		for name, cl := range adminCfg.Clusters {
			clusterName = name
			clusterInfo = cl
			break
		}
	}
	if clusterInfo == nil {
		fatalf("no cluster found in admin kubeconfig")
	}

	// Build user kubeconfig.
	contextName := c.Username + "@" + clusterName
	userCfg := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {
				Server:                   clusterInfo.Server,
				CertificateAuthorityData: clusterInfo.CertificateAuthorityData,
				TLSServerName:            "127.0.0.1",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			c.Username: {
				Token: token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {
				Cluster:  clusterName,
				AuthInfo: c.Username,
			},
		},
		CurrentContext: contextName,
	}

	data, err := serializeKubeconfig(userCfg)
	fatalIf(err)
	fatalIf(writeKubeconfigFile(c.Output, data))
	fmt.Printf("kubeconfig written to %s\n", c.Output)
	return nil
}

type UserRotateCmd struct {
	Username       string        `name:"username" required:"" help:"Username."`
	Kubeconfig     string        `name:"kubeconfig" required:"" help:"Path to admin kubeconfig."`
	Context        string        `name:"context" help:"Kubeconfig context to use."`
	Expiration     time.Duration `name:"expiration" default:"8760h" help:"Token expiration duration."`
	UserKubeconfig string        `name:"user-kubeconfig" required:"" help:"Path to user kubeconfig to update."`
}

func (c *UserRotateCmd) Run() error {
	cs := mustClientset(c.Kubeconfig, c.Context)
	ctx := context.Background()

	// Create new token.
	expSecs := int64(c.Expiration.Seconds())
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expSecs,
		},
	}
	result, err := cs.CoreV1().ServiceAccounts(userNamespace).CreateToken(ctx, c.Username, tokenReq, metav1.CreateOptions{})
	fatalIf(err)
	newToken := result.Status.Token
	fmt.Printf("new token created (expires in %s)\n", c.Expiration)

	// Load user kubeconfig and update token.
	userCfg, err := clientcmd.LoadFromFile(c.UserKubeconfig)
	fatalIf(err)

	updated := false
	if authInfo, ok := userCfg.AuthInfos[c.Username]; ok {
		authInfo.Token = newToken
		updated = true
	} else {
		for name, authInfo := range userCfg.AuthInfos {
			authInfo.Token = newToken
			updated = true
			fmt.Fprintf(os.Stderr, "warning: user %q not found in kubeconfig, updated %q instead\n", c.Username, name)
			break
		}
	}
	if !updated {
		fatalf("no auth entries found in %s", c.UserKubeconfig)
	}

	data, err := serializeKubeconfig(userCfg)
	fatalIf(err)
	fatalIf(writeKubeconfigFile(c.UserKubeconfig, data))
	fmt.Printf("token rotated in %s\n", c.UserKubeconfig)
	return nil
}

type UserCleanupCmd struct {
	Username   string `name:"username" required:"" help:"Username."`
	Kubeconfig string `name:"kubeconfig" required:"" help:"Path to admin kubeconfig."`
	Context    string `name:"context" help:"Kubeconfig context to use."`
}

func (c *UserCleanupCmd) Run() error {
	cs := mustClientset(c.Kubeconfig, c.Context)
	ctx := context.Background()

	// Delete ClusterRoleBinding.
	crbName := "lambdactl-" + c.Username
	err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, crbName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		fatalIf(err)
	}
	fmt.Printf("clusterrolebinding/%s deleted\n", crbName)

	// Delete ServiceAccount.
	err = cs.CoreV1().ServiceAccounts(userNamespace).Delete(ctx, c.Username, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		fatalIf(err)
	}
	fmt.Printf("serviceaccount/%s deleted from %s\n", c.Username, userNamespace)
	return nil
}
