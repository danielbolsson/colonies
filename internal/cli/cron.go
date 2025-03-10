package cli

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"

	"github.com/colonyos/colonies/pkg/client"
	"github.com/colonyos/colonies/pkg/core"
	"github.com/colonyos/colonies/pkg/security"
	"github.com/colonyos/colonies/pkg/server"
	"github.com/kataras/tablewriter"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func init() {
	cronCmd.AddCommand(addCronCmd)
	cronCmd.AddCommand(delCronCmd)
	cronCmd.AddCommand(getCronCmd)
	cronCmd.AddCommand(getCronsCmd)
	cronCmd.AddCommand(runCronCmd)
	rootCmd.AddCommand(cronCmd)

	cronCmd.PersistentFlags().StringVarP(&ServerHost, "host", "", "localhost", "Server host")
	cronCmd.PersistentFlags().IntVarP(&ServerPort, "port", "", -1, "Server HTTP port")

	addCronCmd.Flags().StringVarP(&RuntimeID, "runtimeid", "", "", "Runtime Id")
	addCronCmd.Flags().StringVarP(&RuntimePrvKey, "runtimeprvkey", "", "", "Runtime private key")
	addCronCmd.Flags().StringVarP(&SpecFile, "spec", "", "", "JSON specification of a Colony workflow")
	addCronCmd.MarkFlagRequired("spec")
	addCronCmd.Flags().StringVarP(&ColonyID, "colonyid", "", "", "Colony Id")
	addCronCmd.Flags().StringVarP(&CronName, "name", "", "", "Cron name")
	addCronCmd.MarkFlagRequired("name")
	addCronCmd.Flags().StringVarP(&CronExpr, "cron", "", "", "Cron expression")
	addCronCmd.Flags().IntVarP(&CronIntervall, "interval", "", -1, "Interval in seconds")
	addCronCmd.Flags().BoolVarP(&CronRandom, "random", "", false, "Schedule a random cron, intervall must be specified")

	delCronCmd.Flags().StringVarP(&RuntimeID, "runtimeid", "", "", "Runtime Id")
	delCronCmd.Flags().StringVarP(&RuntimePrvKey, "runtimeprvkey", "", "", "Runtime private key")
	delCronCmd.Flags().StringVarP(&CronID, "cronid", "", "", "Cron Id")
	delCronCmd.MarkFlagRequired("cronid")

	getCronCmd.Flags().StringVarP(&RuntimeID, "runtimeid", "", "", "Runtime Id")
	getCronCmd.Flags().StringVarP(&RuntimePrvKey, "runtimeprvkey", "", "", "Runtime private key")
	getCronCmd.Flags().StringVarP(&CronID, "cronid", "", "", "Cron Id")
	getCronCmd.MarkFlagRequired("cronid")

	getCronsCmd.Flags().StringVarP(&RuntimeID, "runtimeid", "", "", "Runtime Id")
	getCronsCmd.Flags().StringVarP(&RuntimePrvKey, "runtimeprvkey", "", "", "Runtime private key")
	getCronsCmd.Flags().StringVarP(&ColonyID, "colonyid", "", "", "Colony Id")
	getCronsCmd.Flags().IntVarP(&Count, "count", "", server.MAX_COUNT, "Number of crons to list")

	runCronCmd.Flags().StringVarP(&RuntimeID, "runtimeid", "", "", "Runtime Id")
	runCronCmd.Flags().StringVarP(&RuntimePrvKey, "runtimeprvkey", "", "", "Runtime private key")
	runCronCmd.Flags().StringVarP(&CronID, "cronid", "", "", "Cron Id")
	runCronCmd.MarkFlagRequired("cronid")
}

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Manage cron processes",
	Long:  "Manage cron processes",
}

var addCronCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a generator to a Colony",
	Long:  "Add a generator to a Colony",
	Run: func(cmd *cobra.Command, args []string) {
		parseServerEnv()

		jsonSpecBytes, err := ioutil.ReadFile(SpecFile)
		CheckError(err)

		jsonStr := "{\"processspecs\":" + string(jsonSpecBytes) + "}"
		workflowSpec, err := core.ConvertJSONToWorkflowSpec(jsonStr)
		CheckError(err)

		if workflowSpec.ColonyID == "" {
			if ColonyID == "" {
				ColonyID = os.Getenv("COLONIES_COLONYID")
			}
			if ColonyID == "" {
				CheckError(errors.New("Unknown Colony Id, please set COLONYID env variable or specify ColonyID in JSON file"))
			}

			workflowSpec.ColonyID = ColonyID
		}

		workflowSpecJSON, err := workflowSpec.ToJSON()
		CheckError(err)

		if workflowSpec.ColonyID == "" {
			if ColonyID == "" {
				ColonyID = os.Getenv("COLONIES_COLONYID")
			}
			if ColonyID == "" {
				CheckError(errors.New("Unknown Colony Id, please set COLONYID env variable or specify ColonyID in JSON file"))
			}

			workflowSpec.ColonyID = ColonyID
		}

		keychain, err := security.CreateKeychain(KEYCHAIN_PATH)
		CheckError(err)

		if RuntimeID == "" {
			RuntimeID = os.Getenv("COLONIES_RUNTIMEID")
		}
		if RuntimeID == "" {
			CheckError(errors.New("Unknown Runtime Id"))
		}

		if RuntimePrvKey == "" {
			RuntimePrvKey, err = keychain.GetPrvKey(RuntimeID)
			CheckError(err)
		}

		log.WithFields(log.Fields{"ServerHost": ServerHost, "ServerPort": ServerPort, "Insecure": Insecure}).Info("Starting a Colonies client")
		client := client.CreateColoniesClient(ServerHost, ServerPort, Insecure, SkipTLSVerify)

		if CronName == "" {
			CheckError(errors.New("Cron name not specified"))
		}

		if CronIntervall == -1 && CronExpr == "-1" {
			CheckError(errors.New("Cron expression or intervall must be specified"))
		}

		cron := core.CreateCron(ColonyID, CronName, CronExpr, CronIntervall, CronRandom, workflowSpecJSON)
		addedCron, err := client.AddCron(cron, RuntimePrvKey)
		CheckError(err)

		log.WithFields(log.Fields{"CronID": addedCron.ID}).Info("Cron added")
	},
}

var delCronCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a cron",
	Long:  "Delete a cron",
	Run: func(cmd *cobra.Command, args []string) {
		parseServerEnv()

		keychain, err := security.CreateKeychain(KEYCHAIN_PATH)
		CheckError(err)

		if RuntimeID == "" {
			RuntimeID = os.Getenv("COLONIES_RUNTIMEID")
		}
		if RuntimeID == "" {
			CheckError(errors.New("Unknown Runtime Id"))
		}

		if RuntimePrvKey == "" {
			RuntimePrvKey, err = keychain.GetPrvKey(RuntimeID)
			CheckError(err)
		}

		log.WithFields(log.Fields{"ServerHost": ServerHost, "ServerPort": ServerPort, "Insecure": Insecure}).Info("Starting a Colonies client")
		client := client.CreateColoniesClient(ServerHost, ServerPort, Insecure, SkipTLSVerify)

		if CronID == "" {
			CheckError(errors.New("Cron Id not specified"))
		}

		err = client.DeleteCron(CronID, RuntimePrvKey)
		CheckError(err)

		log.WithFields(log.Fields{"CronId": CronID}).Info("Deleting cron")
	},
}

var getCronCmd = &cobra.Command{
	Use:   "get",
	Short: "Get info about a cron",
	Long:  "Get info about a cron",
	Run: func(cmd *cobra.Command, args []string) {
		parseServerEnv()

		keychain, err := security.CreateKeychain(KEYCHAIN_PATH)
		CheckError(err)

		if RuntimeID == "" {
			RuntimeID = os.Getenv("COLONIES_RUNTIMEID")
		}
		if RuntimeID == "" {
			CheckError(errors.New("Unknown Runtime Id"))
		}

		if RuntimePrvKey == "" {
			RuntimePrvKey, err = keychain.GetPrvKey(RuntimeID)
			CheckError(err)
		}

		log.WithFields(log.Fields{"ServerHost": ServerHost, "ServerPort": ServerPort, "Insecure": Insecure}).Info("Starting a Colonies client")
		client := client.CreateColoniesClient(ServerHost, ServerPort, Insecure, SkipTLSVerify)

		if CronID == "" {
			CheckError(errors.New("Cron Id not specified"))
		}

		cron, err := client.GetCron(CronID, RuntimePrvKey)
		if cron == nil {
			log.WithFields(log.Fields{"CronId": CronID}).Error("Cron not found")
			os.Exit(0)
		}

		fmt.Println("Cron:")
		generatorData := [][]string{
			[]string{"Id", cron.ID},
			[]string{"ColonyID", cron.ColonyID},
			[]string{"Name", cron.Name},
			[]string{"Cron Expression", cron.CronExpression},
			[]string{"Interval", strconv.Itoa(cron.Interval)},
			[]string{"Random", strconv.FormatBool(cron.Random)},
			[]string{"NextRun", cron.NextRun.Format(TimeLayout)},
			[]string{"LastRun", cron.LastRun.Format(TimeLayout)},
			[]string{"Last known WorflowID", cron.LastProcessGraphID},
		}
		generatorTable := tablewriter.NewWriter(os.Stdout)
		for _, v := range generatorData {
			generatorTable.Append(v)
		}
		generatorTable.SetAlignment(tablewriter.ALIGN_LEFT)
		generatorTable.SetAutoWrapText(false)
		generatorTable.Render()

		fmt.Println()
		fmt.Println("WorkflowSpec:")
		workflowSpec, err := core.ConvertJSONToWorkflowSpec(cron.WorkflowSpec)
		CheckError(err)
		for i, procesSpec := range workflowSpec.ProcessSpecs {
			fmt.Println()
			fmt.Println("ProcessSpec " + strconv.Itoa(i) + ":")
			printProcessSpec(&procesSpec)
		}
	},
}

var getCronsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all crons in a colony",
	Long:  "List all crons in a colony",
	Run: func(cmd *cobra.Command, args []string) {
		parseServerEnv()

		keychain, err := security.CreateKeychain(KEYCHAIN_PATH)
		CheckError(err)

		if ColonyID == "" {
			ColonyID = os.Getenv("COLONIES_COLONYID")
		}
		if ColonyID == "" {
			CheckError(errors.New("Unknown Colony Id"))
		}

		if RuntimeID == "" {
			RuntimeID = os.Getenv("COLONIES_RUNTIMEID")
		}
		if RuntimeID == "" {
			CheckError(errors.New("Unknown Runtime Id"))
		}

		if RuntimePrvKey == "" {
			RuntimePrvKey, err = keychain.GetPrvKey(RuntimeID)
			CheckError(err)
		}

		log.WithFields(log.Fields{"ServerHost": ServerHost, "ServerPort": ServerPort, "Insecure": Insecure}).Info("Starting a Colonies client")
		client := client.CreateColoniesClient(ServerHost, ServerPort, Insecure, SkipTLSVerify)

		crons, err := client.GetCrons(ColonyID, Count, RuntimePrvKey)
		if crons == nil {
			log.WithFields(log.Fields{"ColonyId": ColonyID}).Info("No crons found")
			os.Exit(0)
		}

		var data [][]string
		for _, cron := range crons {
			data = append(data, []string{cron.ID, cron.Name})
		}
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"CronId", "Name"})
		for _, v := range data {
			table.Append(v)
		}
		table.SetAlignment(tablewriter.ALIGN_LEFT)
		table.Render()
	},
}

var runCronCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a cron now",
	Long:  "Run a cron now",
	Run: func(cmd *cobra.Command, args []string) {
		parseServerEnv()

		keychain, err := security.CreateKeychain(KEYCHAIN_PATH)
		CheckError(err)

		if RuntimeID == "" {
			RuntimeID = os.Getenv("COLONIES_RUNTIMEID")
		}
		if RuntimeID == "" {
			CheckError(errors.New("Unknown Runtime Id"))
		}

		if RuntimePrvKey == "" {
			RuntimePrvKey, err = keychain.GetPrvKey(RuntimeID)
			CheckError(err)
		}

		log.WithFields(log.Fields{"ServerHost": ServerHost, "ServerPort": ServerPort, "Insecure": Insecure}).Info("Starting a Colonies client")
		client := client.CreateColoniesClient(ServerHost, ServerPort, Insecure, SkipTLSVerify)

		if CronID == "" {
			CheckError(errors.New("Cron Id not specified"))
		}

		_, err = client.RunCron(CronID, RuntimePrvKey)
		CheckError(err)

		log.WithFields(log.Fields{"CronID": CronID}).Info("Running cron")
	},
}
