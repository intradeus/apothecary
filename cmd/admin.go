/*
Copyright © 2023 Lucas Roland
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// createCmd represents the create command
var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Create a grimoire structure for your project",
	Long:  `This command will create a basic file structure for your grimoires, including some templates for you to start with.`,
	Run: func(cmd *cobra.Command, args []string) {
		if Debug {
			fmt.Println("admin called")
		}
	},
}

func init() {
	rootCmd.AddCommand(adminCmd)
	var Circles, Name string

	adminCmd.Flags().StringVarP(&Name, "name", "n", "my-grimoires", "The name of the folder containing your grimoires")
	adminCmd.Flags().StringVarP(&Circles, "circles", "c", "developers", "A list of the circles (groups) you want to create for your grimoires ( ex: -c developers,testers,devops )")

}
