package app

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/b97en/wevva/internal/db"
)

// PrintHistory writes the observations recorded since `since`, and the
// stored forecast from now on, as tables. Temperatures are in °C.
func PrintHistory(w io.Writer, dbPath string, since, now time.Time) error {
	store, err := db.Open(dbPath, dbTimeout)
	if err != nil {
		return err
	}
	defer store.Close()

	obs, err := store.Observations(since, now.Add(time.Second))
	if err != nil {
		return err
	}
	forecast, err := store.Forecast(now.Truncate(time.Hour), now.Add(Horizon+time.Hour))
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Observations since %s\n\n", since.Local().Format("Mon 2 Jan 15:04"))
	if len(obs) == 0 {
		fmt.Fprint(w, "  (none)\n")
	} else {
		tw := table(w)
		fmt.Fprintln(tw, "time\tindoor\toutdoor\theating\tsetpoint\tmode\t")
		for _, o := range obs {
			heating := "no"
			if o.Heating {
				heating = "yes"
			}
			fmt.Fprintf(tw, "%s\t%.1f\t%.1f\t%s\t%.1f\t%s\t\n",
				o.Time.Local().Format("Mon 15:04"), o.Indoor, o.Outdoor, heating, o.Setpoint, o.Mode)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	fmt.Fprint(w, "\nOutdoor forecast\n\n")
	if len(forecast) == 0 {
		fmt.Fprint(w, "  (none)\n")
		return nil
	}
	tw := table(w)
	fmt.Fprintln(tw, "time\tfeels like\t")
	for _, h := range forecast {
		fmt.Fprintf(tw, "%s\t%.1f\t\n", h.Time.Local().Format("Mon 15:04"), h.FeelsLike)
	}
	return tw.Flush()
}

func table(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
}
