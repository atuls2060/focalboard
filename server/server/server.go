package server

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/mattermost/focalboard/server/api"
	"github.com/mattermost/focalboard/server/app"
	"github.com/mattermost/focalboard/server/auth"
	"github.com/mattermost/focalboard/server/context"
	appModel "github.com/mattermost/focalboard/server/model"
	"github.com/mattermost/focalboard/server/services/config"
	"github.com/mattermost/focalboard/server/services/scheduler"
	"github.com/mattermost/focalboard/server/services/store"
	"github.com/mattermost/focalboard/server/services/store/sqlstore"
	"github.com/mattermost/focalboard/server/services/telemetry"
	"github.com/mattermost/focalboard/server/services/webhook"
	"github.com/mattermost/focalboard/server/web"
	"github.com/mattermost/focalboard/server/ws"
	"github.com/mattermost/mattermost-server/utils"
	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/services/filesstore"

	"github.com/pkg/errors"
)

type Server struct {
	config              *config.Configuration
	wsServer            *ws.Server
	webServer           *web.Server
	store               store.Store
	filesBackend        filesstore.FileBackend
	telemetry           *telemetry.Service
	logger              *zap.Logger
	cleanUpSessionsTask *scheduler.ScheduledTask

	localRouter     *mux.Router
	localModeServer *http.Server
}

func New(cfg *config.Configuration, singleUser bool) (*Server, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, err
	}

	store, err := sqlstore.New(cfg.DBType, cfg.DBConfigString)
	if err != nil {
		log.Fatal("Unable to start the database", err)
		return nil, err
	}

	auth := auth.New(cfg, store)

	wsServer := ws.NewServer(auth, singleUser)

	filesBackendSettings := model.FileSettings{}
	filesBackendSettings.SetDefaults(false)
	filesBackendSettings.Directory = &cfg.FilesPath
	filesBackend, appErr := filesstore.NewFileBackend(&filesBackendSettings, false)
	if appErr != nil {
		log.Fatal("Unable to initialize the files storage")

		return nil, errors.New("unable to initialize the files storage")
	}

	webhookClient := webhook.NewClient(cfg)

	appBuilder := func() *app.App { return app.New(cfg, store, auth, wsServer, filesBackend, webhookClient) }
	api := api.NewAPI(appBuilder, singleUser)

	// Local router for admin APIs
	localRouter := mux.NewRouter()
	api.RegisterAdminRoutes(localRouter)

	// Init workspace
	appBuilder().GetRootWorkspace()

	webServer := web.NewServer(cfg.WebPath, cfg.Port, cfg.UseSSL, cfg.LocalOnly)
	webServer.AddRoutes(wsServer)
	webServer.AddRoutes(api)

	// Ctrl+C handling
	handler := make(chan os.Signal, 1)
	signal.Notify(handler, os.Interrupt)

	go func() {
		for sig := range handler {
			// sig is a ^C, handle it
			if sig == os.Interrupt {
				os.Exit(1)

				break
			}
		}
	}()

	// Init telemetry
	settings, err := store.GetSystemSettings()
	if err != nil {
		return nil, err
	}

	telemetryID := settings["TelemetryID"]
	if len(telemetryID) == 0 {
		telemetryID = uuid.New().String()
		err := store.SetSystemSetting("TelemetryID", uuid.New().String())
		if err != nil {
			return nil, err
		}
	}

	registeredUserCount, err := appBuilder().GetRegisteredUserCount()
	if err != nil {
		return nil, err
	}

	dailyActiveUsers, err := appBuilder().GetDailyActiveUsers()
	if err != nil {
		return nil, err
	}

	weeklyActiveUsers, err := appBuilder().GetWeeklyActiveUsers()
	if err != nil {
		return nil, err
	}

	monthlyActiveUsers, err := appBuilder().GetMonthlyActiveUsers()
	if err != nil {
		return nil, err
	}

	telemetryService := telemetry.New(telemetryID, zap.NewStdLog(logger))
	telemetryService.RegisterTracker("server", func() map[string]interface{} {
		return map[string]interface{}{
			"version":          appModel.CurrentVersion,
			"build_number":     appModel.BuildNumber,
			"build_hash":       appModel.BuildHash,
			"edition":          appModel.Edition,
			"operating_system": runtime.GOOS,
		}
	})
	telemetryService.RegisterTracker("config", func() map[string]interface{} {
		return map[string]interface{}{
			"serverRoot":  cfg.ServerRoot == config.DefaultServerRoot,
			"port":        cfg.Port == config.DefaultPort,
			"useSSL":      cfg.UseSSL,
			"dbType":      cfg.DBType,
			"single_user": singleUser,
		}
	})
	telemetryService.RegisterTracker("activity", func() map[string]interface{} {
		return map[string]interface{}{
			"registered_users":     registeredUserCount,
			"daily_active_users":   dailyActiveUsers,
			"weekly_active_users":  weeklyActiveUsers,
			"monthly_active_users": monthlyActiveUsers,
		}
	})

	return &Server{
		config:       cfg,
		wsServer:     wsServer,
		webServer:    webServer,
		store:        store,
		filesBackend: filesBackend,
		telemetry:    telemetryService,
		logger:       logger,
		localRouter:  localRouter,
	}, nil
}

func (s *Server) Start() error {
	httpServerExitDone := &sync.WaitGroup{}
	httpServerExitDone.Add(1)

	s.webServer.Start(httpServerExitDone)

	if s.config.EnableLocalMode {
		if err := s.startLocalModeServer(); err != nil {
			return err
		}
	}

	s.cleanUpSessionsTask = scheduler.CreateRecurringTask("cleanUpSessions", func() {
		secondsAgo := int64(60 * 60 * 24 * 31)
		if secondsAgo < s.config.SessionExpireTime {
			secondsAgo = s.config.SessionExpireTime
		}
		if err := s.store.CleanUpSessions(secondsAgo); err != nil {
			s.logger.Error("Unable to clean up the sessions", zap.Error(err))
		}
	}, 10*time.Minute)

	if s.config.Telemetry {
		firstRun := utils.MillisFromTime(time.Now())
		s.telemetry.RunTelemetryJob(firstRun)
	}

	httpServerExitDone.Wait()

	return nil
}

func (s *Server) Shutdown() error {
	if err := s.webServer.Shutdown(); err != nil {
		return err
	}

	s.stopLocalModeServer()

	if s.cleanUpSessionsTask != nil {
		s.cleanUpSessionsTask.Cancel()
	}

	s.telemetry.Shutdown()

	return s.store.Shutdown()
}

func (s *Server) Config() *config.Configuration {
	return s.config
}

// Local server

func (s *Server) startLocalModeServer() error {
	s.localModeServer = &http.Server{
		Handler:     s.localRouter,
		ConnContext: context.SetContextConn,
	}

	// TODO: Close and delete socket file on shutdown
	syscall.Unlink(s.config.LocalModeSocketLocation)

	socket := s.config.LocalModeSocketLocation
	unixListener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}

	go func() {
		log.Println("Starting unix socket server")
		err = s.localModeServer.Serve(unixListener)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("Error starting unix socket server: %v", err)
		}
	}()

	return nil
}

func (s *Server) stopLocalModeServer() {
	if s.localModeServer != nil {
		s.localModeServer.Close()
		s.localModeServer = nil
	}
}
