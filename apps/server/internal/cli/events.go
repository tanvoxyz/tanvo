package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

func newEvents(env *Env, file *string) *cobra.Command {
	events := &cobra.Command{
		Use:   "events",
		Short: "Operate on the stored events",
		Args:  cobra.NoArgs,
	}
	events.AddCommand(newReprocess(env, file))
	return events
}

// newReprocess is `tanvo events reprocess` (lago events:reprocess).
func newReprocess(env *Env, file *string) *cobra.Command {
	var in app.ReprocessInput
	cmd := &cobra.Command{
		Use:   "reprocess",
		Short: "Find the subscriptions whose events need re-enrichment, and re-enrich them",
		Long: "List the active subscriptions of --organization with a charge, or a charge filter, created after the\n" +
			"subscription started, with the metric codes of those charges: their events may have been enriched\n" +
			"under another definition of the metric. With --reprocess, enqueue one job per subscription that\n" +
			"enriches its stored events again, --batch-size events at a time with a --sleep pause between batches,\n" +
			"and stores them back; the workers run the jobs. Without it, the command is a dry run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			cfg, tel, err := setup(ctx, env, *file, "events")
			if err != nil {
				return err
			}
			defer shutdownTelemetry(ctx, tel, &err)
			found, err := app.ReprocessEvents(ctx, cfg, in, tel.TracerProvider, tel.MeterProvider)
			if err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: events reprocess", "error", err)
				return err
			}
			mode := "DRY RUN"
			if in.Reprocess {
				mode = "REPROCESS"
			}
			out := cmd.OutOrStdout()
			if len(found) == 0 {
				_, err = fmt.Fprintf(out, "[%s] no subscriptions need reprocessing\n", mode)
				return err
			}
			for _, c := range found {
				if _, err := fmt.Fprintf(out, "[%s] subscription %s: %s\n", mode, c.SubscriptionID, strings.Join(c.Codes, ", ")); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(out, "[%s] %d subscriptions detected\n", mode, len(found))
			return err
		},
	}
	cmd.Flags().StringVar(&in.OrganizationID, "organization", "", "ID of the organization (required)")
	cmd.Flags().BoolVar(&in.Reprocess, "reprocess", false, "enqueue the re-enrichment (default: dry run)")
	cmd.Flags().IntVar(&in.BatchSize, "batch-size", ingest.DefaultReEnrichBatchSize, "events per batch, at most 1000")
	cmd.Flags().DurationVar(&in.Sleep, "sleep", ingest.DefaultReEnrichSleep, "pause after each batch")
	_ = cmd.MarkFlagRequired("organization")
	return cmd
}
