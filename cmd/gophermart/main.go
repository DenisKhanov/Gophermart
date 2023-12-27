package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/DenisKhanov/Gophermart/internal/app/config"
	"github.com/DenisKhanov/Gophermart/internal/app/handlers"
	"github.com/DenisKhanov/Gophermart/internal/app/logcfg"
	"github.com/DenisKhanov/Gophermart/internal/app/repositories"
	"github.com/DenisKhanov/Gophermart/internal/app/services"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func init() {
	// Установка флага для сериализации в JSON без кавычек
	decimal.MarshalJSONWithoutQuotes = true
}
func main() {
	var (
		dbPool               *pgxpool.Pool
		err                  error
		cfg                  *config.ENVConfig
		GophermartRepository services.Repository
	)

	cfg = config.NewConfig()
	confPool, err := pgxpool.ParseConfig(cfg.EnvDataBase)
	if err != nil {
		logrus.Errorf("error parsing config: %v", err)
	}
	confPool.MaxConns = 50
	confPool.MinConns = 10
	dbPool, err = pgxpool.NewWithConfig(context.Background(), confPool)
	if err != nil {
		logrus.Error("Don't connect to dbPool: ", err)
		os.Exit(1)
	}

	defer dbPool.Close()
	GophermartRepository = repositories.NewURLInDBRepo(dbPool)

	logcfg.RunLoggerConfig(cfg.EnvLogLevel)
	logrus.Infof("Server started:\nServer addres %s\nBase URL %s\nFile path %s\nDBConfig %s\n", cfg.EnvServAdr, cfg.EnvAccrualSystemAddress, cfg.EnvStoragePath, cfg.EnvDataBase)

	GophermartService := services.NewGmartServices(GophermartRepository, cfg.EnvAccrualSystemAddress, dbPool)
	GophermartHandler := handlers.NewHandlers(GophermartService, dbPool)

	router := gin.Default()

	//Public middleware routers group
	publicRoutes := router.Group("/api/user")
	publicRoutes.Use(GophermartHandler.MiddlewareLogging())
	publicRoutes.Use(GophermartHandler.MiddlewareCompress())

	publicRoutes.POST("/register", GophermartHandler.CreateUser)
	publicRoutes.POST("/login", GophermartHandler.LogIn)

	//Private middleware routers group
	privateRoutes := router.Group("/api/user")
	privateRoutes.Use(GophermartHandler.MiddlewareAuthPrivate())
	privateRoutes.Use(GophermartHandler.MiddlewareLogging())
	privateRoutes.Use(GophermartHandler.MiddlewareCompress())

	privateRoutes.POST("/orders", GophermartHandler.InputUserOrder)
	privateRoutes.GET("/orders", GophermartHandler.GetUserOrdersInfo)
	privateRoutes.GET("/balance", GophermartHandler.GetUserBalance)
	privateRoutes.POST("/balance/withdraw", GophermartHandler.WithdrawalBonusForNewOrder)
	privateRoutes.GET("/withdrawals", GophermartHandler.GetUserWithdrawalsInfo)

	server := &http.Server{Addr: cfg.EnvServAdr, Handler: router}

	logrus.Info("Starting server on: ", cfg.EnvServAdr)

	go func() {
		//TODO определить подходящий контекст
		ctx := context.Background()
		for {
			_ = GophermartService.RunUpdateOrdersStatusJob(ctx)
			time.Sleep(1 * time.Second)
		}
	}()

	go func() {
		if err = server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			logrus.Error(err)
		}
	}()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	<-signalChan

	logrus.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = server.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP server Shutdown: %v\n", err)
	}

	logrus.Info("Server exited")
}
