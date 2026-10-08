// Commande depdash : scanne des projets locaux et sert un tableau de bord
// de l'état de leurs dépendances.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/enrich"
	composerparser "github.com/efcoubel/depdash/internal/parser/composer"
	npmparser "github.com/efcoubel/depdash/internal/parser/npm"
	"github.com/efcoubel/depdash/internal/registry"
	"github.com/efcoubel/depdash/internal/registry/endoflife"
	npmregistry "github.com/efcoubel/depdash/internal/registry/npm"
	"github.com/efcoubel/depdash/internal/registry/packagist"
	"github.com/efcoubel/depdash/internal/scanner"
	"github.com/efcoubel/depdash/internal/scheduler"
	"github.com/efcoubel/depdash/internal/status"
	"github.com/efcoubel/depdash/internal/store"
	"github.com/efcoubel/depdash/internal/web"
)

const usage = `Usage : depdash <commande>

  serve         lance le tableau de bord et les scans périodiques (défaut)
  scan          scanne une fois et affiche l'inventaire ; -offline saute les registres
  healthcheck   interroge /healthz du serveur local (pour Docker)

La configuration passe par les variables d'environnement DEPDASH_*.
`

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration invalide", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "serve":
		err = serve(ctx, cfg, log)
	case "scan":
		flags := flag.NewFlagSet("scan", flag.ExitOnError)
		offline := flags.Bool("offline", false, "ne pas interroger les registres")
		flags.Parse(os.Args[2:])
		err = scan(ctx, cfg, log, *offline)
	case "healthcheck":
		err = healthcheck(ctx, cfg)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error(command, "error", err)
		os.Exit(1)
	}
}

// app assemble les briques communes à serve et scan. Ajouter un écosystème
// revient à ajouter ici son Parser et son Registry.
func app(cfg config.Config, log *slog.Logger, offline bool) (*store.Store, *scheduler.Scheduler, config.Mapping, error) {
	mapping, err := config.LoadMapping(cfg.MappingFile)
	if err != nil {
		return nil, nil, mapping, fmt.Errorf("table de correspondance : %w", err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, nil, mapping, fmt.Errorf("ouverture de la base : %w", err)
	}
	sched := &scheduler.Scheduler{
		Scanner: scanner.New(cfg.ProjectsDir, cfg.MaxDepth, cfg.Ignore, composerparser.New(), npmparser.New()),
		Store:   st,
		Mapping: mapping,
		Log:     log,
	}
	if !offline {
		// Un client par registre : chacun a sa propre limite de concurrence.
		sched.Enricher = enrich.New(st, endoflife.New(registry.NewClient(), ""), mapping, log,
			packagist.New(registry.NewClient(), ""),
			npmregistry.New(registry.NewClient(), ""),
		)
	}
	return st, sched, mapping, nil
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	st, sched, mapping, err := app(cfg, log, false)
	if err != nil {
		return err
	}
	defer st.Close()

	server := &web.Server{Store: st, Scheduler: sched, Mapping: mapping, EOLWarning: cfg.EOLWarning, Log: log}
	httpServer := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	sched.Start(ctx, cfg.ScanInterval)
	log.Info("depdash démarré", "port", cfg.Port, "projects", cfg.ProjectsDir, "interval", cfg.ScanInterval)

	errc := make(chan error, 1)
	go func() { errc <- httpServer.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func scan(ctx context.Context, cfg config.Config, log *slog.Logger, offline bool) error {
	st, sched, _, err := app(cfg, log, offline)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := sched.ScanAll(ctx, false); err != nil {
		return err
	}

	projects, err := st.Projects(ctx)
	if err != nil {
		return err
	}
	deps, err := st.DirectDependencies(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJET\tÉCOSYSTÈME\tRUNTIME\tFRAMEWORK\tDIRECTES\tEN RETARD")
	for _, p := range projects {
		if p.ScanError != "" {
			fmt.Fprintf(w, "%s\t%s\tERREUR : %s\n", p.Title(), p.Ecosystem, p.ScanError)
			continue
		}
		late := 0
		for _, d := range deps[p.ID] {
			if d.Cached {
				if st, _ := status.Package(d.Installed, d.Constraint, d.Release); st >= status.MinorUpdate {
					late++
				}
			}
		}
		lateText := strconv.Itoa(late)
		if offline {
			lateText = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", p.Title(), p.Ecosystem,
			dash(p.Runtime+" "+p.RuntimeVersion), dash(p.Framework+" "+p.FrameworkVersion),
			len(deps[p.ID]), lateText)
	}
	return w.Flush()
}

func dash(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "-"
	}
	return s
}

// healthcheck sert de sonde au conteneur : l'image distroless n'embarque ni
// shell ni curl.
func healthcheck(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(cfg.Port)+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
