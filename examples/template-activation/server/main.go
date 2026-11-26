package main

import (
	"context"
	"log"
	"os"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cklauncher"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
)

func main() {
	spec := ckframework.NewApp("template-activation", "0.1.0").
		WithPhases(ckframework.StandardArcadeLoop()).
		WithForm(
			ckframework.TextField("name", "Name").RequiredField().MaxLength(32),
			ckframework.HandleField("ig_username", "Instagram").WithPrefix("@").MaxLength(30),
			ckframework.EmailField("email", "Email").RequiredField().MaxLength(120),
		).
		WithSettings(
			ckframework.EnumSetting("game.mode", "Game", "Game mode", []ckframework.EnumOption{
				{Value: "endless", Label: "Endless"},
				{Value: "timed", Label: "Timed"},
			}, "endless").ApplyNextRound(),
			ckframework.NumberSetting("game.round_seconds", "Game", "Round length", 10, 180, 5, "s", 45).
				ApplyNextRound().VisibleIfRule("game.mode", "timed"),
			ckframework.BoolSetting("display.fullscreen", "Display", "Fullscreen", true).ApplyImmediate(),
			ckframework.BoolSetting("display.show_hitboxes", "Display", "Show hitboxes", false).
				RuntimeOnly().ApplyImmediate(),
			ckframework.IntSetting("display.leaderboard_limit", "Display", "Leaderboard rows", 3, 20, 1, "rows", 8).
				ApplyImmediate(),
			ckframework.NumberSetting("audio.sfx_volume_db", "Audio", "SFX gain", -40, 6, 1, "dB", 0).
				ApplyImmediate(),
		).
		WithActions(
			ckframework.Action("game.recalibrate", "Tracking", "Recalibrate sensor").
				TargetGame().Cooldown(3000),
			ckframework.Action("round.force_cancel", "Game", "Cancel active round").
				TargetServer().Danger().
				ConfirmDialog("Cancel the current player's round?").
				EnabledInPhases(ckframework.PhaseAwaitingHand, ckframework.PhaseCountdown, ckframework.PhasePlaying),
			ckframework.Action("launcher.restart_game", "System", "Restart game").
				TargetLauncher().Danger().
				ConfirmDialog("Restart the game window now?"),
		).
		WithBoards(
			ckframework.NewBoard("top_scores", "Leaderboard").
				WithColumns(
					ckframework.RankColumn("#"),
					ckframework.FieldColumn("name", "Player"),
					ckframework.NumberColumn("score", "Score"),
				).
				SortBy("score", ckframework.SortDesc).
				WithMinScore(1).
				LimitFromSetting("display.leaderboard_limit", 8).
				WithRowDelete(true).
				WithBoardClear(true),
		)

	srv, err := ckserver.New(spec,
		ckserver.WithDatabase("./data/kiosk.db"),
		ckserver.WithKeyFile("./data/server.key"),
		ckserver.WithDefaultOperator("operator", "operator"),
		ckserver.WithVersions(cklauncher.FullVersion(), "0.1.0"),
		ckserver.WithTelemetryIngest(os.Getenv("CK_TELEMETRY_URL"), os.Getenv("CK_TELEMETRY_API_KEY")),
		ckserver.WithDevMode(os.Getenv("CK_DEV") == "1"),
	)
	if err != nil {
		log.Fatalf("init ckserver: %v", err)
	}
	defer srv.Close(context.Background())

	if err := cklauncher.Run(srv, cklauncher.Options{
		ListenAddr:        ":8080",
		GameBinary:        os.Getenv("CK_GAME_BINARY"),
		ManifestPath:      "./kiosk.manifest.json",
		CountdownSeconds:  30,
		AutoRestartOnExit: true,
	}); err != nil {
		log.Fatalf("cklauncher exit: %v", err)
	}
}
