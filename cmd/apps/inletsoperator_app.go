package apps

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"

	"github.com/alexellis/k3sup/pkg"
	"github.com/alexellis/k3sup/pkg/config"
	"github.com/alexellis/k3sup/pkg/env"
	"github.com/alexellis/k3sup/pkg/helm"
	"github.com/spf13/cobra"
)

func MakeInstallInletsOperator() *cobra.Command {
	var inletsOperator = &cobra.Command{
		Use:          "inlets-operator",
		Short:        "Install inlets-operator",
		Long:         `Install inlets-operator to get public IPs for your cluster`,
		Example:      `  k3sup app install inlets-operator --namespace default`,
		SilenceUsage: true,
	}

	inletsOperator.Flags().StringP("namespace", "n", "default", "The namespace used for installation")
	inletsOperator.Flags().StringP("license", "l", "", "The license key if using inlets-pro")
	inletsOperator.Flags().StringP("provider", "p", "digitalocean", "Your infrastructure provider - 'packet', 'digitalocean', 'scaleway', 'gce' or 'ec2'")
	inletsOperator.Flags().StringP("zone", "z", "us-central1-a", "The zone to provision the exit node (Used by GCE")
	inletsOperator.Flags().String("project-id", "", "Project ID to be used (for GCE and Packet)")
	inletsOperator.Flags().StringP("region", "r", "lon1", "The default region to provision the exit node (DigitalOcean, Packet and Scaleway")
	inletsOperator.Flags().String("organization-id", "", "The organization id (Scaleway")
	inletsOperator.Flags().StringP("token-file", "t", "", "Text file containing token or a service account JSON file")
	inletsOperator.Flags().Bool("update-repo", true, "Update the helm repo")

	inletsOperator.Flags().String("pro-client-image", "", "Docker image for inlets-pro's client")
	inletsOperator.Flags().Bool("helm3", true, "Use helm3, if set to false uses helm2")
	inletsOperator.Flags().StringArray("set", []string{}, "Use custom flags or override existing flags \n(example --set=image=org/repo:tag)")

	inletsOperator.RunE = func(command *cobra.Command, args []string) error {
		kubeConfigPath := getDefaultKubeconfig()

		if command.Flags().Changed("kubeconfig") {
			kubeConfigPath, _ = command.Flags().GetString("kubeconfig")
		}

		fmt.Printf("Using kubeconfig: %s\n", kubeConfigPath)

		helm3, _ := command.Flags().GetBool("helm3")

		if helm3 {
			fmt.Println("Using helm3")
		}

		namespace, _ := command.Flags().GetString("namespace")

		if namespace != "default" {
			return fmt.Errorf(`to override the namespace, install inlets-operator via helm manually`)
		}

		arch := getNodeArchitecture()
		fmt.Printf("Node architecture: %q\n", arch)

		userPath, err := config.InitUserDir()
		if err != nil {
			return err
		}

		clientArch, clientOS := env.GetClientArch()

		fmt.Printf("Client: %q, %q\n", clientArch, clientOS)

		log.Printf("User dir established as: %s\n", userPath)

		os.Setenv("HELM_HOME", path.Join(userPath, ".helm"))

		if helm3 {
			os.Setenv("HELM_VERSION", helm3Version)
		}
		_, err = helm.TryDownloadHelm(userPath, clientArch, clientOS, helm3)
		if err != nil {
			return err
		}

		err = addHelmRepo("inlets", "https://inlets.github.io/inlets-operator/", helm3)
		if err != nil {
			return err
		}

		updateRepo, _ := inletsOperator.Flags().GetBool("update-repo")

		if updateRepo {
			err = updateHelmRepos(helm3)
			if err != nil {
				return err
			}
		}

		chartPath := path.Join(os.TempDir(), "charts")

		err = fetchChart(chartPath, "inlets/inlets-operator", helm3)
		if err != nil {
			return err
		}
		overrides, err := getInletsOperatorOverrides(command)

		if err != nil {
			return err
		}

		_, err = kubectlTask("apply", "-f", "https://raw.githubusercontent.com/inlets/inlets-operator/master/artifacts/crd.yaml")
		if err != nil {
			return err
		}

		secretFileName, _ := command.Flags().GetString("token-file")

		if len(secretFileName) == 0 {
			return fmt.Errorf(`--token-file is a required field for your cloud API token or service account JSON file`)
		}

		res, err := kubectlTask("create", "secret", "generic",
			"inlets-access-key",
			"--from-file", "inlets-access-key="+secretFileName)

		if len(res.Stderr) > 0 && strings.Contains(res.Stderr, "AlreadyExists") {
			fmt.Println("[Warning] secret inlets-access-key already exists and will be used.")
		} else if len(res.Stderr) > 0 {
			return fmt.Errorf("Error from kubectl\n%q", res.Stderr)
		} else if err != nil {
			return err
		}

		customFlags, _ := command.Flags().GetStringArray("set")

		if err := mergeFlags(overrides, customFlags); err != nil {
			return err
		}

		region, _ := command.Flags().GetString("region")
		overrides["region"] = region

		if val, _ := command.Flags().GetString("license"); len(val) > 0 {
			overrides["inletsProLicense"] = val
		}

		if val, _ := command.Flags().GetString("pro-client-image"); len(val) > 0 {
			overrides["proClientImage"] = val
		}

		if helm3 {
			wait := false

			outputPath := path.Join(chartPath, "inlets-operator")

			err := helm3Upgrade(outputPath, "inlets/inlets-operator",
				namespace, "values.yaml", overrides, wait)
			if err != nil {
				return err
			}

		} else {
			outputPath := path.Join(chartPath, "inlets-operator/rendered")
			err = templateChart(chartPath, "inlets-operator", namespace, outputPath, "values.yaml", overrides)
			if err != nil {
				return err
			}

			applyRes, applyErr := kubectlTask("apply", "-R", "-f", outputPath)
			if applyErr != nil {
				return applyErr
			}

			if applyRes.ExitCode > 0 {
				return fmt.Errorf("Error applying templated YAML files, error: %s", applyRes.Stderr)
			}
		}
		fmt.Println(inletsOperatorPostInstallMsg)

		return nil
	}

	return inletsOperator
}

func getInletsOperatorOverrides(command *cobra.Command) (map[string]string, error) {
	overrides := map[string]string{}
	provider, _ := command.Flags().GetString("provider")
	overrides["provider"] = strings.ToLower(provider)

	providers := []string{
		"digitalocean", "packet", "ec2", "scaleway", "gce",
	}
	found := false
	for _, p := range providers {
		if p == provider {
			found = true
		}
	}
	if !found {
		return overrides, fmt.Errorf("provider: %s not supported at this time", provider)
	}

	if provider == "gce" {
		gceProjectID, err := command.Flags().GetString("project-id")
		if err != nil {
			return overrides, err
		}
		overrides["gceProjectId"] = gceProjectID

		zone, err := command.Flags().GetString("zone")
		if err != nil {
			return overrides, err
		}
		overrides["zone"] = strings.ToLower(zone)

		if len(zone) == 0 {
			return overrides, fmt.Errorf("zone is required for provider %s", provider)
		}

		if len(gceProjectID) == 0 {
			return overrides, fmt.Errorf("project-id is required for provider %s", provider)
		}
	} else if provider == "packet" {
		packetProjectID, err := command.Flags().GetString("project-id")
		if err != nil {
			return overrides, err
		}
		overrides["packetProjectId"] = packetProjectID

		if len(packetProjectID) == 0 {
			return overrides, fmt.Errorf("project-id is required for provider %s", provider)
		}

	} else if provider == "scaleway" {
		orgID, err := command.Flags().GetString("organization-id")
		if err != nil {
			return overrides, err
		}
		overrides["organization-id"] = orgID

		if len(orgID) == 0 {
			return overrides, fmt.Errorf("organization-id is required for provider %s", provider)
		}
	}

	return overrides, nil
}

const InletsOperatorInfoMsg = `# The default configuration is for DigitalOcean and your secret is
# stored as "inlets-access-key" in the "default" namespace.

# To get your first Public IP run the following:
kubectl run nginx-1 --image=nginx --port=80 --restart=Always
kubectl expose deployment nginx-1 --port=80 --type=LoadBalancer

# Find your IP in the "EXTERNAL-IP" field, watch for "<pending>" to 
# change to an IP

kubectl get svc -w

# When you're done, remove the tunnel by deleting the service
kubectl delete svc/nginx-1

# Check the logs
kubectl logs deploy/inlets-operator -f

# Find out more at:
# https://github.com/inlets/inlets-operator`

const inletsOperatorPostInstallMsg = `=======================================================================
= inlets-operator has been installed.                                  =
=======================================================================` +
	"\n\n" + InletsOperatorInfoMsg + "\n\n" + pkg.ThanksForUsing
