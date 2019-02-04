package delete

import (
	"fmt"
	"os"
	"strings"

	"github.com/kris-nova/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha4"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
	"github.com/weaveworks/eksctl/pkg/eks"
	"github.com/weaveworks/eksctl/pkg/printers"
	"github.com/weaveworks/eksctl/pkg/utils/kubeconfig"
)

var (
	clusterConfigFile = ""
)

func deleteClusterCmd(g *cmdutils.Grouping) *cobra.Command {
	p := &api.ProviderConfig{}
	cfg := api.NewClusterConfig()

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Delete a cluster",
		Run: func(cmd *cobra.Command, args []string) {
			if err := doDeleteCluster(p, cfg, cmdutils.GetNameArg(args), cmd); err != nil {
				logger.Critical("%s\n", err.Error())
				os.Exit(1)
			}
		},
	}

	group := g.New(cmd)

	group.InFlagSet("General", func(fs *pflag.FlagSet) {
		fs.StringVarP(&cfg.Metadata.Name, "name", "n", "", "EKS cluster name (required)")
		cmdutils.AddRegionFlag(fs, p)
		cmdutils.AddWaitFlag(&wait, fs)
		fs.StringVarP(&clusterConfigFile, "config-file", "f", "", "load configuration from a file")
	})

	cmdutils.AddCommonFlagsForAWS(group, p, true)

	group.AddTo(cmd)
	return cmd
}

func doDeleteCluster(p *api.ProviderConfig, cfg *api.ClusterConfig, nameArg string, cmd *cobra.Command) error {
	meta := cfg.Metadata

	printer := printers.NewJSONPrinter()

	if err := api.Register(); err != nil {
		return err
	}

	if clusterConfigFile != "" {
		if err := eks.LoadConfigFromFile(clusterConfigFile, cfg); err != nil {
			return err
		}
		meta = cfg.Metadata

		incompatibleFlags := []string{
			"name",
			"region",
		}

		for _, f := range incompatibleFlags {
			if cmd.Flag(f).Changed {
				return fmt.Errorf("cannot use --%s when --config-file/-f is set", f)
			}
		}

		if nameArg != "" {
			return fmt.Errorf("cannot use name argument %q when --config-file/-f is set")
		}

		if meta.Name == "" {
			return fmt.Errorf("metadata.name must be set")
		}

		if meta.Region == "" {
			return fmt.Errorf("metadata.region must be set")
		}

		p.Region = meta.Region
	} else {
		if cfg.Metadata.Name != "" && nameArg != "" {
			return cmdutils.ErrNameFlagAndArg(cfg.Metadata.Name, nameArg)
		}

		if nameArg != "" {
			cfg.Metadata.Name = nameArg
		}

		if cfg.Metadata.Name == "" {
			return fmt.Errorf("--name must be set")
		}
	}

	ctl := eks.New(p, cfg)

	if err := ctl.CheckAuth(); err != nil {
		return err
	}

	logger.Info("deleting EKS cluster %q", cfg.Metadata.Name)
	if err := printer.LogObj(logger.Debug, "cfg.json = \\\n", cfg); err != nil {
		return err
	}

	var deletedResources []string

	handleIfError := func(err error, name string) bool {
		if err != nil {
			logger.Debug("continue despite error: %v", err)
			return true
		}
		logger.Debug("deleted %q", name)
		deletedResources = append(deletedResources, name)
		return false
	}

	// We can remove all 'DeprecatedDelete*' calls in 0.2.0

	stackManager := ctl.NewStackManager(cfg)

	{
		errs := stackManager.WaitDeleteAllNodeGroups()
		if len(errs) > 0 {
			logger.Info("%d error(s) occurred while deleting nodegroup(s)", len(errs))
			for _, err := range errs {
				logger.Critical("%s\n", err.Error())
			}
			return fmt.Errorf("failed to delete nodegroup(s)")
		}
		logger.Debug("all nodegroups were deleted")
	}

	var clusterErr bool
	if wait {
		clusterErr = handleIfError(stackManager.WaitDeleteCluster(), "cluster")
	} else {
		clusterErr = handleIfError(stackManager.DeleteCluster(), "cluster")
	}

	if clusterErr {
		if handleIfError(ctl.DeprecatedDeleteControlPlane(cfg.Metadata), "control plane") {
			handleIfError(stackManager.DeprecatedDeleteStackControlPlane(wait), "stack control plane (deprecated)")
		}
	}

	handleIfError(stackManager.DeprecatedDeleteStackServiceRole(wait), "service group (deprecated)")
	handleIfError(stackManager.DeprecatedDeleteStackVPC(wait), "stack VPC (deprecated)")
	handleIfError(stackManager.DeprecatedDeleteStackDefaultNodeGroup(wait), "default nodegroup (deprecated)")

	ctl.MaybeDeletePublicSSHKey(cfg.Metadata.Name)

	kubeconfig.MaybeDeleteConfig(cfg.Metadata)

	if len(deletedResources) == 0 {
		logger.Warning("no EKS cluster resources were found for %q", cfg.Metadata.Name)
	} else {
		logger.Success("the following EKS cluster resource(s) for %q will be deleted: %s. If in doubt, check CloudFormation console", cfg.Metadata.Name, strings.Join(deletedResources, ", "))
	}

	return nil
}
