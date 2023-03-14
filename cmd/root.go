package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var Debug bool

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "apo",
	Version: "0.0.1",
	Short:   "Apothecary",
	Long: `Apothecary, a CLI tool to easily concoct local development environment. With Apothecary, you can :
- Create different, complex configuration ("grimoires") to connect multiple tools together and locally launch all your services at once (vscode, dev containers, docker, podman, docker-compose, networks, scripts, etc.)
- Create initial configurations for your new project contributors: a simple command and they are ready to code.
- (Admin) create different groups ("circles") of contributors, manage existing configs or add new ones in seconds. Keep all your contributors up to date with the latest tools and configurations.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Here you will define your persistant and local flags and configuration settings.
	rootCmd.PersistentFlags().BoolVarP(&Debug, "debug", "d", false, "Debug mode")
	rootCmd.SetVersionTemplate("🧪 Apothecary🧪 by Intradeus \n{{.Version}}")

}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	CfgName := "apothecary.yaml"

	// Search config in current directory with name ".grimoires".
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetConfigName(CfgName)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found; Create it
			err := viper.SafeWriteConfigAs(CfgName)
			if err != nil {
				panic(fmt.Errorf("Error while creating file %s : %w", viper.ConfigFileUsed(), err))
			}
		} else {
			// Config file was found but another error was produced
			panic(fmt.Errorf("Fatal error with config file (%s) : %w", viper.ConfigFileUsed(), err)) // or %q ?
		}
	}

	fmt.Printf("grimoires: %v\n", viper.Get("grimoires"))
	fmt.Printf("circles: %v\n", viper.Get("circles"))
	// viper.AutomaticEnv() // read in environment variables that match
}
