package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DenisKhanov/Gophermart/internal/app/auth"
	"github.com/DenisKhanov/Gophermart/internal/app/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode"
)

// Repository defines the interface for interacting with the storage backend.
//
//go:generate mockgen -source=service.go -destination=mocks/service_mock.go -package=mocks
type Repository interface {
	StoreNewUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID, login string, hashedPassword []byte) error
	StoreNewUserBalance(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error
	GetUserHashPassword(ctx context.Context, login string) ([]byte, error)
	GetUUIDFromOrders(ctx context.Context, orderNumber string) (uuid.UUID, error)
	GetUUIDFromUsers(ctx context.Context, login string) (uuid.UUID, error)
	StoreUserOrder(ctx context.Context, tx pgx.Tx, orderNumber, orderStatus string, userID uuid.UUID, bonus decimal.Decimal) error
	GetProcessingOrders(ctx context.Context) ([]models.UserOrder, error)
	GetUserProcessingOrders(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error)
	UpdateOrders(ctx context.Context, tx pgx.Tx, orders []models.AccrualResponseData) error
	GetUserOrders(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error)
	StoreUserWithdrawal(ctx context.Context, tx pgx.Tx, userID uuid.UUID, orderNumber string, sum decimal.Decimal) error
	GetUserBalance(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error)
	GetUserWithdrawn(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error)
	UpdateUserBalance(ctx context.Context, tx pgx.Tx, userID uuid.UUID, newBalance decimal.Decimal) error
	UsersBalanceUpdate(ctx context.Context, tx pgx.Tx, usersBalanceToUpdate map[uuid.UUID]decimal.Decimal) error
	GetUserWithdrawals(ctx context.Context, userID uuid.UUID) ([]models.UserWithdrawal, error)
}

type GmartServices struct {
	repository     Repository
	accrualAddress string
	dbPool         *pgxpool.Pool //opened in main func dbPool pool connections
}

func NewGmartServices(repository Repository, accrualAddress string, dbPool *pgxpool.Pool) *GmartServices {
	return &GmartServices{
		repository:     repository,
		accrualAddress: accrualAddress,
		dbPool:         dbPool,
	}
}

// isValidLuhn проверяет, действителен ли номер согласно алгоритму Луна.
func isValidLuhn(number string) bool {
	var sum int
	nDigits := len(number)
	parity := nDigits % 2

	for i, r := range number {
		digit, _ := strconv.Atoi(string(r))

		if i%2 == parity {
			digit = digit * 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

// CreateUser метод регистрации пользователя, выполняет проверки на качество логина и пароля
// и в случае соответствия сохраняет пользователя в базу данных
func (s GmartServices) CreateUser(ctx context.Context, login, password string) (token string, err error) {
	//Пришлось выключить, данные условия не заложены в автотесты((
	//if err = s.checkRegistrationData(login, password); err != nil {
	//	logrus.Error(err)
	//	return "", err
	//}
	if len(login) < 1 || len(password) < 1 {
		return "", models.ErrSaveNewUser
	}
	_, err = s.repository.GetUserHashPassword(ctx, login)
	if err == nil {
		logrus.Error(models.ErrUserAlreadyTaken)
		return "", models.ErrUserAlreadyTaken
	}
	hashedPassword, err := auth.CreateHashPassword(password)
	if err != nil {
		logrus.Error(err)
		return "", err
	}
	userID := auth.GenerateUniqueID()
	token, err = auth.BuildJWTString(userID)
	if err != nil {
		return "", models.ErrSaveNewUser
	}
	if err = s.withTransaction(ctx, func(tx pgx.Tx) error {
		if err = s.repository.StoreNewUser(ctx, tx, userID, login, hashedPassword); err != nil {
			logrus.Error(models.ErrSaveNewUser)
			return models.ErrSaveNewUser
		}
		if err = s.repository.StoreNewUserBalance(ctx, tx, userID); err != nil {
			logrus.Error(models.ErrSaveNewUser)
			return models.ErrSaveNewUser
		}
		return nil
	}); err != nil {
		return "", err
	}
	return token, nil
}

// LogIn метод аутентификации пользователя, в случае успеха возвращает token
func (s GmartServices) LogIn(ctx context.Context, login, password string) (token string, err error) {
	//Пришлось выключить, данные условия не заложены в автотесты((
	//if err = s.checkLogin(login); err != nil {
	//	logrus.Error(err)
	//	return "", err
	//}
	savedHashedPassword, err := s.repository.GetUserHashPassword(ctx, login)
	if err != nil {
		logrus.Error(err)
		return "", err
	}
	if auth.CheckHashPasswordForValid(savedHashedPassword, password) {
		savedUserID, err := s.repository.GetUUIDFromUsers(ctx, login)
		if err != nil {
			return "", models.ErrAccessingDB
		}
		token, err = auth.BuildJWTString(savedUserID)
		if err != nil {
			return "", models.ErrSaveNewUser
		}
		return token, nil
	}
	return "", models.ErrUnauthorizedUser
}

// checkRegistrationData объединяет checkLogin и checkPassword, если логин или пароль не соответствует, то возвращает ошибку
func (s GmartServices) checkRegistrationData(login, password string) error {
	if err := s.checkLogin(login); err != nil {
		logrus.Error(err)
		return err
	}
	return s.checkPassword(password)
}

// checkLogin метод проверки логина на соответствие критериям (длина не меньше 5 и не больше 20 символов,
// и не должен содержать символов кроме "-" и "_")
func (s GmartServices) checkLogin(login string) error {
	var (
		hasMinLen     = false
		hasMaxLen     = false
		hasValidChars = false
	)

	const minLen = 5
	const maxLen = 20

	// Check login length
	l := len(login)
	if l >= minLen {
		hasMinLen = true
	}
	if l <= maxLen {
		hasMaxLen = true
	}

	// Checking for valid characters
	isAlphanum := regexp.MustCompile(`^[A-Za-z0-9_\-]+$`).MatchString
	if isAlphanum(login) {
		hasValidChars = true
	}
	if !hasMinLen {
		return errors.New("the login is too short")
	}
	if !hasMaxLen {
		return errors.New("the login is too long")
	}
	if !hasValidChars {
		return errors.New("the login contains invalid characters")
	}
	return nil
}

// checkPassword метод проверки пароля на соответствие критериям (длина не меньше 8 символов,
// должен содержать хоть одну строчную букву, хоть одну цифру, хоть один символ)
func (s GmartServices) checkPassword(password string) error {
	var (
		hasMinLen  = false
		hasUpper   = false
		hasLower   = false
		hasNumber  = false
		hasSpecial = false
	)

	const minLen = 8

	// Check password length
	if len(password) >= minLen {
		hasMinLen = true
	}

	for _, char := range password {
		switch {
		case unicode.IsUpper(char):
			hasUpper = true
		case unicode.IsLower(char):
			hasLower = true
		case unicode.IsNumber(char):
			hasNumber = true
		case unicode.IsPunct(char) || unicode.IsSymbol(char):
			hasSpecial = true
		}
	}

	if !hasMinLen {
		return errors.New("пароль слишком короткий")
	}
	if !hasUpper {
		return errors.New("пароль должен содержать хотя бы одну заглавную букву")
	}
	if !hasLower {
		return errors.New("пароль должен содержать хотя бы одну строчную букву")
	}
	if !hasNumber {
		return errors.New("пароль должен содержать хотя бы одну цифру")
	}
	if !hasSpecial {
		return errors.New("пароль должен содержать хотя бы один специальный символ")
	}
	return nil
}

// InputUserOrder метод принимает номер заказа, проверяет его при помощи алгоритма Луна и если все ок,
// то асинхронно отправляет запрос в accrualAPI сервис и записывает результат в базу
func (s GmartServices) InputUserOrder(ctx context.Context, userID uuid.UUID, orderNumber string) error {
	if !isValidLuhn(orderNumber) {
		return models.ErrOrderNumber
	}
	savedUserID, err := s.repository.GetUUIDFromOrders(ctx, orderNumber)
	if err == nil {
		if savedUserID == userID {
			return models.ErrUserOrderExists
		}
		return models.ErrAnotherUserOrderExists
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		logrus.Error(err)
		return models.ErrAccessingDB
	}
	resultCh := make(chan models.AccrualResponseData)
	errCh := make(chan error)
	var order models.UserOrder
	order.Number = orderNumber
	go func() {
		defer close(resultCh)
		defer close(errCh)
		responseData, err := s.GetAccrualAPI(ctx, order)
		if err != nil {
			errCh <- err
			return
		} else {
			resultCh <- responseData
		}
	}()
	var accrualData models.AccrualResponseData
	select {
	case accrualData = <-resultCh:
		if accrualData.Status == "REGISTERED" {
			accrualData.Status = "NEW"
		}
		var userBalance = decimal.NewFromFloat(0.00)
		userBalance, err = s.repository.GetUserBalance(ctx, userID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logrus.Error(err)
				return models.ErrAccessingDB
			}
		}
		newUserBalance := userBalance.Add(*accrualData.Accrual)
		return s.withTransaction(ctx, func(tx pgx.Tx) error {
			if err = s.repository.StoreUserOrder(ctx, tx, orderNumber, accrualData.Status, userID, *accrualData.Accrual); err != nil {
				logrus.Error(err)
				return models.ErrAccessingDB
			}
			if accrualData.Accrual != nil {
				if err = s.repository.UpdateUserBalance(ctx, tx, userID, newUserBalance); err != nil {
					logrus.Error(err)
					return models.ErrAccessingDB
				}
				return nil
			}
			return nil
		})
	case err = <-errCh:
		if err != nil {
			logrus.Error(err)
			return err
		}
	}
	return nil
}

// withTransaction создание и управление транзакцией
func (s GmartServices) withTransaction(ctx context.Context, txFunc func(pgx.Tx) error) error {
	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		logrus.Error("Error starting transaction: ", err)
		return err
	}
	//управление транзакцией при помощи замыкания
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback(ctx)
			panic(p)
		} else if err != nil {
			tx.Rollback(ctx)
		} else {
			err = tx.Commit(ctx)
		}
	}()

	err = txFunc(tx)
	return err
}

// startWorkers создание воркер пула
func (s GmartServices) startWorkers(ctx context.Context, numOfWorkers int, tasks <-chan models.UserOrder, resultCh chan<- models.AccrualResponseData, errCh chan<- error, wg *sync.WaitGroup) {
	for i := 0; i < numOfWorkers; i++ {
		go s.worker(ctx, tasks, resultCh, errCh, wg)
	}
}

// startWorkers создание воркера
func (s GmartServices) worker(ctx context.Context, tasks <-chan models.UserOrder, resultCh chan<- models.AccrualResponseData, errCh chan<- error, wg *sync.WaitGroup) {
	for task := range tasks {
		responseData, err := s.GetAccrualAPI(ctx, task)
		if err != nil {
			errCh <- err
		} else {
			if task.Status != responseData.Status {
				resultCh <- responseData
			}
		}
		wg.Done()
	}
}

// RunUpdateOrdersStatusJob метод запускающий сбор заказов с незавершенными статусами и обращение с ними к accrual сервису
func (s GmartServices) RunUpdateOrdersStatusJob(ctx context.Context) error {
	processingOrders, err := s.repository.GetProcessingOrders(ctx)
	if err != nil {
		return err
	}

	tasks := make(chan models.UserOrder, len(processingOrders))
	resultCh := make(chan models.AccrualResponseData)
	errCh := make(chan error)
	var wg sync.WaitGroup

	numOfWorkers := 10 // Устанавливаем количество воркеров
	s.startWorkers(ctx, numOfWorkers, tasks, resultCh, errCh, &wg)
	for _, order := range processingOrders {
		wg.Add(1)
		tasks <- order
	}
	close(tasks)

	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()
	done := false
	var ordersToUpdate []models.AccrualResponseData
	var usersBalanceToUpdate = make(map[uuid.UUID]decimal.Decimal)
	for !done {
		select {
		case accrualData, ok := <-resultCh:
			if !ok {
				resultCh = nil
			} else {
				ordersToUpdate = append(ordersToUpdate, accrualData)
				if accrualData.Accrual != nil {
					userBalance, err := s.repository.GetUserBalance(ctx, accrualData.UserID)
					if err != nil {
						logrus.Error(err)
						return models.ErrAccessingDB
					}
					usersBalanceToUpdate[accrualData.UserID] = userBalance.Add(*accrualData.Accrual)
				}
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
			} else {
				logrus.Error(err)
				return err
			}
		}
		if resultCh == nil && errCh == nil {
			done = true
		}
	}

	// запускаем транзакцию в которой обновляем баланс пользователя и состояние заказов
	return s.withTransaction(ctx, func(tx pgx.Tx) error {
		if err = s.repository.UsersBalanceUpdate(ctx, tx, usersBalanceToUpdate); err != nil {
			logrus.Error(err)
			return models.ErrAccessingDB
		}
		// Обновление заказов в таблице processingOrders базы данных
		if len(ordersToUpdate) > 0 {
			if err = s.repository.UpdateOrders(ctx, tx, ordersToUpdate); err != nil {
				return models.ErrAccessingDB
			}
		}
		return nil
	})
}

// CheckUpdateUserOrders метод запускающий сбор заказов с незавершенными статусами у конкретного пользователя
// и обращение с ними к accrual сервису для проверки обновленного состояния
func (s GmartServices) CheckUpdateUserOrders(ctx context.Context, userID uuid.UUID) error {
	orders, err := s.repository.GetUserProcessingOrders(ctx, userID)
	if err != nil {
		return err
	}
	var ordersToUpdate []models.AccrualResponseData
	tasks := make(chan models.UserOrder, len(orders))
	resultCh := make(chan models.AccrualResponseData)
	errCh := make(chan error)
	var wg sync.WaitGroup

	numOfWorkers := 10 // Устанавливаем количество воркеров
	s.startWorkers(ctx, numOfWorkers, tasks, resultCh, errCh, &wg)
	for _, order := range orders {
		wg.Add(1)
		tasks <- order
	}
	close(tasks)

	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()
	done := false
	var accrualToUpdate decimal.Decimal
	for !done {
		select {
		case accrualData, ok := <-resultCh:
			if !ok {
				resultCh = nil
			} else {
				ordersToUpdate = append(ordersToUpdate, accrualData)
				if accrualData.Accrual != nil {
					accrualToUpdate = accrualToUpdate.Add(*accrualData.Accrual)
				}
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
			} else {
				logrus.Error(err)
				return err
			}
		}
		if resultCh == nil && errCh == nil {
			done = true
		}
	}
	// получаем нынешний баланс бонусов пользователя
	userBalance, err := s.repository.GetUserBalance(ctx, userID)
	if err != nil {
		logrus.Error(err)
		return models.ErrAccessingDB
	}
	// запускаем транзакцию в которой обновляем баланс пользователя и состояние заказов
	newUserBalance := userBalance.Add(accrualToUpdate)
	return s.withTransaction(ctx, func(tx pgx.Tx) error {
		if err = s.repository.UpdateUserBalance(ctx, tx, userID, newUserBalance); err != nil {
			logrus.Error(err)
			return models.ErrAccessingDB
		}
		// Обновление заказов в таблице orders базы данных
		if len(ordersToUpdate) > 0 {
			if err = s.repository.UpdateOrders(ctx, tx, ordersToUpdate); err != nil {
				return models.ErrAccessingDB
			}
		}
		return nil
	})

}

// GetAccrualAPI отправляет запрос в систему расчёта баллов лояльности
// и возвращает models.AccrualResponseData.
func (s GmartServices) GetAccrualAPI(ctx context.Context, order models.UserOrder) (models.AccrualResponseData, error) {
	accrualSystemURL := s.accrualAddress + "/api/orders/" + order.Number
	logrus.Info(accrualSystemURL)

	// create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accrualSystemURL, nil)
	if err != nil {
		logrus.Error("failed to create request to accrual system: ", err)
		return models.AccrualResponseData{}, err
	}
	// sent request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logrus.Error("failed to send request to accrual system: ", err)
		return models.AccrualResponseData{}, err
	}
	defer resp.Body.Close()

	// check request status
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusNoContent {
		return models.AccrualResponseData{UserID: order.UserID, Order: order.Number, Status: "NEW"}, nil
	}
	if resp.StatusCode == http.StatusInternalServerError {
		logrus.Error("internal server error: ", resp.Status)
		return models.AccrualResponseData{}, fmt.Errorf("internal server error: %s", resp.Status)
	}

	// read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.Error("failed to read response body: ", err)
		return models.AccrualResponseData{}, err
	}

	// parse body
	var accrualResponse models.AccrualResponseData
	logrus.Info(string(body))
	if err = json.Unmarshal(body, &accrualResponse); err != nil {
		logrus.Error("failed to unmarshal response body: ", err)
		return models.AccrualResponseData{}, err
	}
	accrualResponse.UserID = order.UserID
	return accrualResponse, nil
}

// GetUserOrdersInfo метод возвращает все заказы пользователя или ошибку models.ErrUserHasNoOrders если у пользователя нет заказов
func (s GmartServices) GetUserOrdersInfo(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error) {
	userOrders, err := s.repository.GetUserOrders(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(userOrders) == 0 {
		return nil, models.ErrUserHasNoOrders
	}
	return userOrders, nil
}

// GetUserBalance получаем из репозитория баланс пользователя и возвращаем в хендлер models.BalanceResponseData или ошибку
func (s GmartServices) GetUserBalance(ctx context.Context, userID uuid.UUID) (userBalance models.BalanceResponseData, err error) {
	balance, err := s.repository.GetUserBalance(ctx, userID)
	if err != nil {
		logrus.Error(err)
		return userBalance, models.ErrAccessingDB
	}
	userWithdrawn, err := s.repository.GetUserWithdrawn(ctx, userID)
	if err != nil {
		logrus.Error(err)
		userWithdrawn = decimal.NewFromFloat(0.00)
	}
	userBalance.Current = balance
	userBalance.Withdrawn = userWithdrawn
	return userBalance, nil
}

// TODO неужели номер заказа на списание не должен быть уникальным?

// WithdrawalBonusForNewOrder сохранение запроса на списание бонусных средств на оплату заказа
func (s GmartServices) WithdrawalBonusForNewOrder(ctx context.Context, userID uuid.UUID, orderNumber string, sum decimal.Decimal) error {
	if !isValidLuhn(orderNumber) {
		return models.ErrOrderNumber
	}
	userBalance, err := s.repository.GetUserBalance(ctx, userID)
	if err != nil {
		logrus.Error(err)
		return models.ErrAccessingDB
	}
	if userBalance.LessThan(sum) {
		return models.ErrNotEnoughFunds
	}

	newUserBalance := userBalance.Sub(sum)

	return s.withTransaction(ctx, func(tx pgx.Tx) error {
		if err = s.repository.StoreUserWithdrawal(ctx, tx, userID, orderNumber, sum); err != nil {
			logrus.Error(err)
			return err
		}
		if err = s.repository.UpdateUserBalance(ctx, tx, userID, newUserBalance); err != nil {
			logrus.Error(err)
			return err
		}
		return nil
	})
}

// GetUserWithdrawalsInfo отображение информации о списаниях пользователя
func (s GmartServices) GetUserWithdrawalsInfo(ctx context.Context, userID uuid.UUID) ([]models.UserWithdrawal, error) {
	userWithdrawals, err := s.repository.GetUserWithdrawals(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(userWithdrawals) == 0 {
		return nil, models.ErrUserHasNoWithdrawals
	}
	return userWithdrawals, nil
}
