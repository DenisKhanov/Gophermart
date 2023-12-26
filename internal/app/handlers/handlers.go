package handlers

import (
	"compress/gzip"
	"context"
	"errors"
	"github.com/DenisKhanov/Gophermart/internal/app/auth"
	"github.com/DenisKhanov/Gophermart/internal/app/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:generate mockgen -source=handlers.go -destination=mocks/handlers_mock.go -package=mocks
type Service interface {
	CreateUser(ctx context.Context, login, password string) (token string, err error)
	LogIn(ctx context.Context, login, password string) (token string, err error)
	InputUserOrder(ctx context.Context, userID uuid.UUID, orderNumber string) error
	GetUserOrdersInfo(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error)
	GetUserBalance(ctx context.Context, userID uuid.UUID) (userBalance models.BalanceResponseData, err error)
	WithdrawalBonusForNewOrder(ctx context.Context, userID uuid.UUID, orderNumber string, sum decimal.Decimal) error
	GetUserWithdrawalsInfo(ctx context.Context, userID uuid.UUID) ([]models.UserWithdrawal, error)
	RunUpdateOrdersStatusJob(ctx context.Context) error
}

type Handlers struct {
	service Service
	DB      *pgxpool.Pool
}
type responseData struct {
	status int
	size   int
}
type loggingResponseWriter struct {
	http.ResponseWriter
	responseData *responseData
}

var typeArray = [2]string{"application/json", "text/html"}

func NewHandlers(service Service, DB *pgxpool.Pool) *Handlers {
	return &Handlers{
		service: service,
		DB:      DB,
	}
}

// CreateUser регистрация нового пользователя
func (h Handlers) CreateUser(c *gin.Context) {
	ctx := c.Request.Context()
	var dataUser models.UserRegistered
	if err := c.ShouldBindJSON(&dataUser); err != nil {
		logrus.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}
	tokenString, err := h.service.CreateUser(ctx, dataUser.Login, dataUser.Password)
	if err != nil {
		if errors.Is(err, models.ErrUserAlreadyTaken) {
			logrus.Error(err)
			c.Status(http.StatusConflict)
			return
		}
		if errors.Is(err, models.ErrSaveNewUser) {
			logrus.Error(err)
			c.Status(http.StatusInternalServerError)
			return
		}
		logrus.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
	c.SetCookie("user_token", tokenString, 0, "/", "", false, true)
}

// LogIn аутентификация пользователя
func (h Handlers) LogIn(c *gin.Context) {
	ctx := c.Request.Context()
	var dataUser models.UserRegistered
	if err := c.ShouldBindJSON(&dataUser); err != nil {
		logrus.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}
	tokenString, err := h.service.LogIn(ctx, dataUser.Login, dataUser.Password)
	if err != nil {
		if errors.Is(err, models.ErrAccessingDB) {
			logrus.Error(err)
			c.Status(http.StatusInternalServerError)
		}
		logrus.Info(err)
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Status(http.StatusOK)
	c.SetCookie("user_token", tokenString, 0, "/", "", false, true)
}

// InputUserOrder загрузка пользователем нового заказа
func (h Handlers) InputUserOrder(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := ctx.Value(models.UserIDKey).(uuid.UUID)
	if !ok {
		logrus.Errorf("context value is not userID: %v", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	orderNumber, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logrus.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error reading request body"})
		return
	}
	if err = h.service.InputUserOrder(ctx, userID, string(orderNumber)); err != nil {
		statusCode, message := errorCodeToStatus(err)
		logrus.Error(err)
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	c.Status(http.StatusAccepted)
}

// errorCodeToStatus вспомогательный метод проверки ошибок и установки статусов
func errorCodeToStatus(err error) (int, string) {
	switch {
	case errors.Is(err, models.ErrOrderNumber):
		return http.StatusUnprocessableEntity, "Invalid order number"
	case errors.Is(err, models.ErrTokenIsNotValid):
		return http.StatusUnauthorized, "Token is not valid"
	case errors.Is(err, models.ErrUserOrderExists):
		return http.StatusOK, "User order already exists"
	case errors.Is(err, models.ErrAnotherUserOrderExists):
		return http.StatusConflict, "Another user's order exists"
	case errors.Is(err, models.ErrAccessingDB):
		return http.StatusInternalServerError, "Error accessing database"
	default:
		return http.StatusInternalServerError, "Internal server error"
	}
}

// GetUserOrdersInfo получение списка загруженных пользователем номеров заказов от старых к новым
func (h Handlers) GetUserOrdersInfo(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := ctx.Value(models.UserIDKey).(uuid.UUID)
	if !ok {
		logrus.Errorf("context value is not userID: %v", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	userOrders, err := h.service.GetUserOrdersInfo(ctx, userID)
	if err != nil {
		if errors.Is(err, models.ErrUserHasNoOrders) {
			logrus.Error(err)
			c.JSON(http.StatusNoContent, gin.H{"error": err.Error()})
			return
		}
		logrus.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, userOrders)
}

func (h Handlers) GetUserBalance(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := ctx.Value(models.UserIDKey).(uuid.UUID)
	if !ok {
		logrus.Errorf("context value is not userID: %v", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	var userBalance models.BalanceResponseData
	userBalance, err := h.service.GetUserBalance(ctx, userID)
	if err != nil {
		logrus.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, userBalance)
}

func (h Handlers) WithdrawalBonusForNewOrder(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := ctx.Value(models.UserIDKey).(uuid.UUID)
	if !ok {
		logrus.Errorf("context value is not userID: %v", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	var withdrawalRequest models.UserWithdrawal
	if err := c.ShouldBindJSON(&withdrawalRequest); err != nil {
		logrus.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.service.WithdrawalBonusForNewOrder(ctx, userID, withdrawalRequest.Order, *withdrawalRequest.Sum); err != nil {
		if errors.Is(err, models.ErrOrderNumber) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, models.ErrNotEnoughFunds) {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (h Handlers) GetUserWithdrawalsInfo(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := ctx.Value(models.UserIDKey).(uuid.UUID)
	if !ok {
		logrus.Errorf("context value is not userID: %v", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	userWithdrawals, err := h.service.GetUserWithdrawalsInfo(ctx, userID)
	if err != nil {
		if errors.Is(err, models.ErrUserHasNoWithdrawals) {
			logrus.Error(err)
			c.JSON(http.StatusNoContent, gin.H{"error": err.Error()})
			return
		}
		logrus.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, userWithdrawals)
}

func (r *loggingResponseWriter) Write(b []byte) (int, error) {
	size, err := r.ResponseWriter.Write(b)
	r.responseData.size += size
	return size, err
}
func (r *loggingResponseWriter) WriteHeader(statusCode int) {
	r.ResponseWriter.WriteHeader(statusCode)
	r.responseData.status = statusCode
}

type compressWriter struct {
	gin.ResponseWriter
	Writer *gzip.Writer
}

func (c *compressWriter) Write(data []byte) (int, error) {
	return c.Writer.Write(data)
}
func (c *compressWriter) Close() error {
	return c.Writer.Close()
}
func (c *compressWriter) WriteString(s string) (int, error) {
	return c.Writer.Write([]byte(s))
}

// MiddlewareLogging provides a logging middleware for Gin.
// It logs details about each request including the URL, method, response status, duration, and size.
// This middleware is useful for monitoring and debugging purposes.
func (h Handlers) MiddlewareLogging() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Запуск таймера
		start := time.Now()

		// Обработка запроса
		c.Next()

		// Измерение времени обработки
		duration := time.Since(start)

		// Получение статуса ответа и размера
		status := c.Writer.Status()
		size := c.Writer.Size()

		// Логирование информации о запросе
		logrus.WithFields(logrus.Fields{
			"url":      c.Request.URL.RequestURI(),
			"method":   c.Request.Method,
			"status":   status,
			"duration": duration,
			"size":     size,
		}).Info("Обработан запрос")
	}
}

// MiddlewareCompress provides a compression middleware using gzip.
// It checks the 'Accept-Encoding' header of incoming requests and applies gzip compression if applicable.
// This middleware optimizes response size and speed, improving overall performance.
func (h Handlers) MiddlewareCompress() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			gz := gzip.NewWriter(c.Writer)
			defer gz.Close()
			c.Writer = &compressWriter{Writer: gz, ResponseWriter: c.Writer}
			c.Header("Content-Encoding", "gzip")
		}
		// Проверяем, сжат ли запрос
		if strings.Contains(c.GetHeader("Content-Encoding"), "gzip") {
			reader, err := gzip.NewReader(c.Request.Body)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid gzip body"})
				return
			}
			defer reader.Close()
			c.Request.Body = reader
		}
		c.Next()
	}
}

// MiddlewareAuthPublic provides authentication middleware for public routes.
// It manages user tokens, generating new tokens if necessary, and adds user ID to the context.
// This middleware is useful for routes that require user identification but not strict authentication.
//func (h Handlers) MiddlewareAuthPublic() gin.HandlerFunc {
//	return func(c *gin.Context) {
//		var tokenString string
//		var err error
//		var userID uint32
//
//		tokenString, err = c.Cookie("user_token")
//		// если токен не найден в куке, то генерируем новый и добавляем его в куки
//		if err != nil || !auth.IsValidToken(tokenString) {
//			logrus.Info("Cookie not found or token in cookie not found")
//			tokenString, err = auth.BuildJWTString()
//			if err != nil {
//				logrus.Errorf("error generating token: %v", err)
//				c.AbortWithStatus(http.StatusInternalServerError)
//			}
//			c.SetCookie("user_token", tokenString, 0, "/", "", false, true)
//		}
//		userID, err = auth.GetUserID(tokenString)
//		if err != nil {
//			logrus.Error(err)
//			return
//		}
//
//		ctx := context.WithValue(c.Request.Context(), models.UserIDKey, userID)
//		c.Request = c.Request.WithContext(ctx)
//		c.Next()
//	}
//}

// MiddlewareAuthPrivate provides authentication middleware for private routes.
// It checks the user token and only allows access if the token is valid.
// This middleware ensures that only authenticated users can access certain routes.
func (h Handlers) MiddlewareAuthPrivate() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString, err := c.Cookie("user_token")
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
		userID, err := auth.GetUserID(tokenString)
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
		ctx := context.WithValue(c.Request.Context(), models.UserIDKey, userID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
