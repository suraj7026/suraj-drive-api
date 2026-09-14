package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/config"
	"surajdrive/backend/internal/database"
	"surajdrive/backend/internal/handler"
	"surajdrive/backend/internal/importer"
	"surajdrive/backend/internal/malware"
	appmiddleware "surajdrive/backend/internal/middleware"
	"surajdrive/backend/internal/notification"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/preview"
	"surajdrive/backend/internal/purge"
	"surajdrive/backend/internal/ratelimit"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
	"surajdrive/backend/internal/upload"
)

const requiredSchemaVersion int64 = 16

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	db, err := database.Open(context.Background(), database.PoolConfig{
		URL:              cfg.Database.URL,
		MaxConnections:   int32(cfg.Database.MaxConnections),
		MinConnections:   int32(cfg.Database.MinConnections),
		HealthTimeout:    time.Duration(cfg.Database.HealthTimeoutSecs) * time.Second,
		MaxConnectionAge: time.Duration(cfg.Database.MaxConnectionAgeMins) * time.Minute,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer db.Close()
	metadata := repository.NewMetadata(db)

	store, err := storage.NewMinIOClient(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to MinIO")
	}

	legacyImporter := importer.NewLegacy(metadata, store)
	authHandler := handler.NewAuthHandler(cfg, store, metadata, legacyImporter)
	cursorCodec := pagination.NewCodec(cfg.JWT.Secret, 24*time.Hour)
	clientIPKey, err := appmiddleware.NewClientIPKey(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to configure trusted proxies")
	}
	fileHandler := handler.NewFileHandler(store, metadata, cursorCodec, cfg.Security.MalwareScanEnabled, !cfg.Server.IsProd, cfg.Server.FrontendURL, clientIPKey)
	metadataHandler := handler.NewMetadataHandler(legacyImporter, metadata, store)
	loginLimiter := ratelimit.NewFixedWindow(20, 15*time.Minute, 100_000)
	authenticatedLimiter := ratelimit.NewFixedWindow(1200, time.Minute, 100_000)
	var malwareScanner *malware.ClamAV
	if cfg.Security.MalwareScanEnabled {
		malwareScanner = malware.NewClamAV(cfg.Security.ClamAVAddress)
	}

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(appmiddleware.RequestIDHeader)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(appmiddleware.SecurityHeaders(cfg.Server.IsProd))
	r.Use(appmiddleware.CORS(cfg.Server.FrontendURL))

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/api/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.Database.HealthTimeoutSecs)*time.Second)
		defer cancel()
		var schemaVersion int64
		if err := db.QueryRow(ctx, `
			SELECT drive.schema_version()
		`).Scan(&schemaVersion); err != nil || schemaVersion != requiredSchemaVersion {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		if err := store.HealthCheck(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		if malwareScanner != nil {
			if err := malwareScanner.Ping(ctx); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"unavailable"}`))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	r.Route("/api/auth", func(r chi.Router) {
		r.With(appmiddleware.RateLimit(loginLimiter, 900, clientIPKey)).Get("/google/login", authHandler.GoogleLogin)
		r.Get("/google/callback", authHandler.GoogleCallback)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata), appmiddleware.RequireTrustedOrigin(cfg.Server.FrontendURL)).Post("/logout", authHandler.Logout)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata)).Get("/me", authHandler.Me)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata)).Get("/sessions", authHandler.ListSessions)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata), appmiddleware.RequireTrustedOrigin(cfg.Server.FrontendURL)).Delete("/sessions/{sessionID}", authHandler.RevokeSessionByID)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata), appmiddleware.RequireTrustedOrigin(cfg.Server.FrontendURL)).Post("/sessions/revoke-others", authHandler.RevokeOtherSessions)
	})
	r.Get("/api/public/links/{token}", fileHandler.GetPublicLink)
	r.Get("/api/public/links/{token}/children", fileHandler.ListPublicLinkChildren)
	r.Get("/api/public/links/{token}/items/{publicItemID}/preview", fileHandler.PreviewPublicLinkItem)
	r.Get("/api/public/links/{token}/items/{publicItemID}/download", fileHandler.DownloadPublicLinkItem)

	r.Route("/api", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata))
		r.Use(appmiddleware.RateLimit(authenticatedLimiter, 60, appmiddleware.AuthenticatedUserOrIPKey(clientIPKey)))
		r.Use(appmiddleware.RequireTrustedOrigin(cfg.Server.FrontendURL))

		r.Get("/files", fileHandler.List)
		r.Post("/uploads", fileHandler.ReserveUpload)
		r.Get("/uploads", fileHandler.ListUploadReservations)
		r.Get("/uploads/{uploadID}", fileHandler.GetUploadReservation)
		r.Post("/uploads/{uploadID}/parts/presign", fileHandler.PresignUploadParts)
		r.Post("/uploads/{uploadID}/complete", fileHandler.CompleteUploadReservation)
		r.Post("/uploads/{uploadID}/abort", fileHandler.AbortUploadReservation)
		r.Post("/folders", fileHandler.CreateFolder)
		if !cfg.Server.IsProd {
			r.Post("/files/upload", fileHandler.Upload)
			r.Post("/files/upload/complete", fileHandler.CompletePresignedUpload)
			r.Delete("/files", fileHandler.Delete)
			r.Post("/files/copy", fileHandler.Copy)
			r.Get("/files/presign/download", fileHandler.PresignDownload)
			r.Get("/files/presign/upload", fileHandler.PresignUpload)
			r.Get("/files/preview", fileHandler.Preview)
			r.Delete("/folders", fileHandler.DeleteFolder)
		}
		r.Get("/search", fileHandler.Search)
		r.Get("/trash", fileHandler.ListTrash)
		r.Delete("/trash", fileHandler.EmptyTrash)
		r.Get("/shared", fileHandler.ListSharedWithMe)
		r.Get("/views/{view}", fileHandler.ListUserView)
		r.Get("/storage/summary", fileHandler.StorageSummary)
		r.Get("/preferences", fileHandler.GetUserPreferences)
		r.Patch("/preferences", fileHandler.UpdateUserPreferences)
		r.Post("/metadata/reconcile", metadataHandler.ReconcileLegacy)
		r.Post("/metadata/audit", metadataHandler.AuditConsistency)
		r.Post("/items/batch", fileHandler.BatchItems)
		r.Post("/items/{itemID}/trash", fileHandler.TrashItem)
		r.Post("/items/{itemID}/restore", fileHandler.RestoreItem)
		r.Delete("/items/{itemID}", fileHandler.PermanentlyDeleteItem)
		r.Get("/items/{itemID}", fileHandler.GetItem)
		r.Post("/items/{itemID}/shortcuts", fileHandler.CreateShortcut)
		r.Get("/items/{itemID}/shortcut", fileHandler.ResolveShortcut)
		r.Get("/items/{itemID}/activity", fileHandler.ListItemActivity)
		r.Get("/items/{itemID}/comments", fileHandler.ListComments)
		r.Post("/items/{itemID}/comments", fileHandler.CreateComment)
		r.Patch("/items/{itemID}/comments/{commentID}", fileHandler.UpdateComment)
		r.Delete("/items/{itemID}/comments/{commentID}", fileHandler.DeleteComment)
		r.Post("/items/{itemID}/comments/{commentID}/resolve", fileHandler.ResolveComment)
		r.Post("/items/{itemID}/comments/{commentID}/reopen", fileHandler.ReopenComment)
		r.Patch("/items/{itemID}", fileHandler.UpdateItem)
		r.Get("/items/{itemID}/download", fileHandler.DownloadItem)
		r.Post("/items/{itemID}/copy", fileHandler.CopyItem)
		r.Get("/items/{itemID}/preview", fileHandler.PreviewItem)
		r.Get("/items/{itemID}/versions", fileHandler.ListFileVersions)
		r.Get("/items/{itemID}/versions/{versionID}/download", fileHandler.DownloadFileVersion)
		r.Post("/items/{itemID}/versions/{versionID}/restore", fileHandler.RestoreFileVersion)
		r.Put("/items/{itemID}/versions/{versionID}/retention", fileHandler.SetVersionRetention)
		r.Put("/items/{itemID}/star", fileHandler.SetItemStarred)
		r.Post("/items/{itemID}/opened", fileHandler.MarkItemOpened)
		r.Get("/items/{itemID}/permissions", fileHandler.ListItemPermissions)
		r.Get("/items/{itemID}/children", fileHandler.ListAccessibleChildren)
		r.Post("/items/{itemID}/permissions", fileHandler.GrantItemPermission)
		r.Delete("/items/{itemID}/permissions/{permissionID}", fileHandler.RevokeItemPermission)
		r.Post("/items/{itemID}/invitations/{invitationID}/resend", fileHandler.ResendShareInvitation)
		r.Delete("/items/{itemID}/invitations/{invitationID}", fileHandler.RevokeShareInvitation)
		r.Get("/items/{itemID}/links", fileHandler.ListShareLinks)
		r.Post("/items/{itemID}/links", fileHandler.CreateShareLink)
		r.Delete("/items/{itemID}/links/{linkID}", fileHandler.RevokeShareLink)
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           r,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSecs) * time.Second,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutSecs) * time.Second,
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeoutSecs) * time.Second,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeoutSecs) * time.Second,
	}

	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	go runUploadCleanup(shutdownSignal, metadata)
	preview.NewWorker(metadata, store, 2).Run(shutdownSignal)
	purge.NewWorker(metadata, store).Run(shutdownSignal)
	upload.NewWorker(metadata, store, cfg.Security.MalwareScanEnabled, 2).Run(shutdownSignal)
	if cfg.Security.MalwareScanEnabled {
		malware.NewWorker(
			metadata, store, malwareScanner,
			time.Duration(cfg.Security.ScanTimeoutSecs)*time.Second, cfg.Security.ScanWorkers,
		).Run(shutdownSignal)
	}
	if cfg.Notifications.Enabled {
		sender := notification.NewSMTPSender(notification.SMTPConfig{
			Address: cfg.Notifications.SMTPAddress, Username: cfg.Notifications.SMTPUsername,
			Password: cfg.Notifications.SMTPPassword, FromAddress: cfg.Notifications.FromAddress,
			FromName: cfg.Notifications.FromName, AllowPlaintext: cfg.Notifications.AllowPlaintext,
		})
		notification.NewWorker(
			metadata, sender, cfg.Notifications.PublicURL,
			time.Duration(cfg.Notifications.TimeoutSecs)*time.Second, cfg.Notifications.Workers,
		).Run(shutdownSignal)
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Info().Str("addr", server.Addr).Msg("server starting")
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("server stopped unexpectedly")
		}
	case <-shutdownSignal.Done():
		log.Info().Msg("shutdown signal received")
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			log.Error().Err(err).Msg("graceful shutdown failed")
			_ = server.Close()
		}
	}
}

func runUploadCleanup(ctx context.Context, metadata *repository.Metadata) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := cleanupExpiredUploads(ctx, metadata); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("expired upload cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cleanupExpiredUploads(ctx context.Context, metadata *repository.Metadata) error {
	uploads, err := metadata.ListExpiredUploads(ctx, 50)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		if err := metadata.MarkUploadExpired(ctx, upload.ID); err != nil {
			log.Warn().Err(err).Str("upload_id", upload.ID).Msg("failed to finalize expired upload")
		}
	}
	return nil
}
