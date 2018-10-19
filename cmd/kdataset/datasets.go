package main

import (
	"errors"
	"fmt"

	"github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
)

type datasetsCmd struct {
	workspace string
}

func NewDatasetsCmd() *cobra.Command {
	datasets := &datasetsCmd{}
	cmd := &cobra.Command{
		Use:   "dataset-list <workspace>",
		Short: "List datasets for current workspace.",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			// Validation
			if len(args) < 1 {
				return errors.New("Too few arguments.")
			}
			workspace := args[0]

			datasets.workspace = workspace

			return datasets.run()
		},
	}

	return cmd
}

func (cmd *datasetsCmd) run() (err error) {
	client, err := initClient()
	if err != nil {
		return err
	}

	logrus.Debug("Run dataset-list...")

	datasets, err := client.ListEntities(entityType.Value, cmd.workspace)
	if err != nil {
		logrus.Fatal(err)
	}

	if len(datasets.Items) == 0 {
		fmt.Println("No datasets.")
		return nil
	}

	fmt.Println("DATASETS:")
	for _, ds := range datasets.Items {
		fmt.Println(ds.Name)
	}
	return
}
