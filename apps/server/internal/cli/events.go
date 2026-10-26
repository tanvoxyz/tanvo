package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

func newEvents(env *Env, file *string) *cobra.Command {
	events := &cobra.Command{
		Use:   "events",
		Short: "Operate on the stored events",
		Args:  cobra.NoArgs,
	}
	events.AddCommand(newReprocess(env, file), newReplayDeadLetters(env, file))
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

// newReplayDeadLetters is `tanvo events replay-dead-letters`.
func newReplayDeadLetters(env *Env, file *string) *cobra.Command {
	var (
		in       app.ReplayInput
		from, to string
	)
	cmd := &cobra.Command{
		Use:   "replay-dead-letters",
		Short: "Count the dead letters of an organization, and produce them again to their topics",
		Long: "Count the dead letters of --organization stored in ClickHouse's events_dead_letter that have not been\n" +
			"replayed, by error code and topic, optionally only those that failed in [--from, --to) with --error-code.\n" +
			"With --replay, produce each one's original message again to its topic, keyed as its producer keys it,\n" +
			"and record the replay, so that it is not replayed again. The event stores drop the copies of an event\n" +
			"they already hold, so a replay is safe to run again. Without --replay, the command is a dry run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			for _, b := range []struct {
				flag, value string
				into        *time.Time
			}{{"--from", from, &in.From}, {"--to", to, &in.To}} {
				if b.value == "" {
					continue
				}
				if *b.into, err = time.Parse(time.RFC3339, b.value); err != nil {
					return fmt.Errorf("%s: %w", b.flag, err)
				}
			}
			cfg, tel, err := setup(ctx, env, *file, "events")
			if err != nil {
				return err
			}
			defer shutdownTelemetry(ctx, tel, &err)
			got, err := app.ReplayDeadLetters(ctx, cfg, in, tel.TracerProvider, tel.MeterProvider, tel.Propagator)
			if err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: events replay-dead-letters", "error", err)
				return err
			}
			mode := "DRY RUN"
			if in.Replay {
				mode = "REPLAY"
			}
			out := cmd.OutOrStdout()
			var total uint64
			for _, c := range got.Pending {
				total += c.Count
				if _, err := fmt.Fprintf(out, "[%s] %s on %s: %d dead letters\n", mode, c.ErrorCode, c.Topic, c.Count); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "[%s] %d dead letters to replay\n", mode, total); err != nil {
				return err
			}
			if in.Replay {
				_, err = fmt.Fprintf(out, "[%s] %d replayed, %d skipped\n", mode, got.Result.Replayed, got.Result.Skipped)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&in.OrganizationID, "organization", "", "ID of the organization (required)")
	cmd.Flags().StringVar(&from, "from", "", "replay the dead letters that failed at or after this RFC 3339 instant")
	cmd.Flags().StringVar(&to, "to", "", "replay the dead letters that failed before this RFC 3339 instant")
	cmd.Flags().StringVar(&in.ErrorCode, "error-code", "", "replay the dead letters with this error code (reject_event, write_events, …)")
	cmd.Flags().BoolVar(&in.Replay, "replay", false, "produce the dead letters again (default: dry run)")
	cmd.Flags().IntVar(&in.BatchSize, "batch-size", kafka.DefaultReplayBatchSize, "messages produced at once")
	_ = cmd.MarkFlagRequired("organization")
	return cmd
}
