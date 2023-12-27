package repositories

// TODO разделить репозиторий на отдельные репозитории

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/DenisKhanov/Gophermart/internal/app/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

type InDBRepo struct {
	dbPool *pgxpool.Pool //opened in main func dbPool pool connections
}

func NewURLInDBRepo(dbPool *pgxpool.Pool) *InDBRepo {
	storage := &InDBRepo{
		dbPool: dbPool,
	}
	if err := storage.CreateBDTables(); err != nil {
		logrus.Error(err)
	}
	return storage
}

// CreateBDTables создание таблиц users и orders в базе данных
func (d *InDBRepo) CreateBDTables() error {
	ctx := context.Background()
	sqlQuery := `
CREATE TABLE users (
    uuid UUID PRIMARY KEY,
    login VARCHAR(255) UNIQUE NOT NULL,
    hashed_password BYTEA NOT NULL,
    date TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    order_number VARCHAR(255) NOT NULL UNIQUE,
    uuid UUID NOT NULL,
    accrual DECIMAL(9, 2),
    status VARCHAR(255) NOT NULL,
    date TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (uuid) REFERENCES users(uuid)
);
CREATE TABLE withdrawals (
    id SERIAL PRIMARY KEY,
    order_number VARCHAR(255) NOT NULL,
    uuid UUID NOT NULL,
    sum DECIMAL(9, 2),
    date TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (uuid) REFERENCES users(uuid)
);
CREATE TABLE balance (
    id SERIAL PRIMARY KEY,
    uuid UUID NOT NULL,
    user_balance DECIMAL(9, 2) DEFAULT 0.00,
    date TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (uuid) REFERENCES users(uuid)
);`
	_, err := d.dbPool.Exec(ctx, sqlQuery)
	if err != nil {
		logrus.Errorf("don't create tables users and orders: %v", err)
		return err
	}
	logrus.Info("Successfully created tables users and orders")
	return nil
}

// StoreNewUser сохраняет нового пользователя (заранее сгенерированный UUID, логин и хешированный пароль)
func (d *InDBRepo) StoreNewUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID, login string, hashedPassword []byte) error {

	const sqlQuery = `INSERT INTO users (uuid,login, hashed_password) VALUES ($1, $2,$3) ON CONFLICT (login) DO NOTHING`
	_, err := tx.Exec(ctx, sqlQuery, userID, login, hashedPassword)
	if err != nil {
		logrus.Error("new user don't save in database ", err)
		return err
	}
	return nil
}

// StoreNewUserBalance создает поле с нулевым балансом для пользователя в таблице balance
func (d *InDBRepo) StoreNewUserBalance(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	const sqlQuery = `INSERT INTO balance (uuid) VALUES ($1)`
	_, err := tx.Exec(ctx, sqlQuery, userID)
	if err != nil {
		logrus.Error("user balance (0.00) don't save in database ", err)
		return err
	}
	return nil
}

// StoreUserOrder сохраняет с привязкой к UUID пользователя новый заказ
func (d *InDBRepo) StoreUserOrder(ctx context.Context, tx pgx.Tx, orderNumber, orderStatus string, userID uuid.UUID, bonus decimal.Decimal) error {
	const sqlQuery = `INSERT INTO orders (order_number, uuid,accrual,status) VALUES ($1, $2,$3,$4)`
	_, err := tx.Exec(ctx, sqlQuery, orderNumber, userID, bonus, orderStatus)
	if err != nil {
		logrus.Error("new order don't save in database ", err)
		return err
	}
	return nil
}

// GetUUID возвращает userID на основании его логина или возвращает ошибку если не существует
func (d *InDBRepo) GetUUIDFromUsers(ctx context.Context, login string) (uuid.UUID, error) {
	const selectQuery = `SELECT uuid FROM users WHERE login = $1`
	var savedUserID uuid.UUID
	err := d.dbPool.QueryRow(ctx, selectQuery, login).Scan(&savedUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logrus.Error(err)
			return uuid.Nil, fmt.Errorf("login not found: %w", err)
		}
		logrus.Error("error querying for uuid: ", err)
		return uuid.Nil, fmt.Errorf("error querying for login: %w", err)
	}
	return savedUserID, nil
}

// GetUserHashPassword возвращает хешированный пароль на основании логина пользователя или возвращает ошибку если не существует
func (d *InDBRepo) GetUserHashPassword(ctx context.Context, login string) ([]byte, error) {
	//TODO может лучше принимать в виде аргумента userID?
	const selectQuery = `SELECT hashed_password FROM users WHERE login = $1`
	var savedHashedPassword []byte
	err := d.dbPool.QueryRow(ctx, selectQuery, login).Scan(&savedHashedPassword)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logrus.Error(err)
			return nil, fmt.Errorf("login not found: %w", err)
		}
		logrus.Error("error querying for savedHashedPassword: ", err)
		return nil, fmt.Errorf("error querying for login: %w", err)
	}
	return savedHashedPassword, nil
}

// GetOrderUUID возвращает сохраненный userID по номеру заказа или возвращает ошибку если не существует
func (d *InDBRepo) GetUUIDFromOrders(ctx context.Context, orderNumber string) (uuid.UUID, error) {
	const selectQuery = `SELECT uuid FROM orders WHERE order_number = $1`
	var savedUserID uuid.UUID
	err := d.dbPool.QueryRow(ctx, selectQuery, orderNumber).Scan(&savedUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logrus.Errorf("the order number not found: %s", err)
			return uuid.Nil, fmt.Errorf("the order number not found: %w", err)
		}
		logrus.Errorf("error querying for uuid: %s", err)
		return uuid.Nil, fmt.Errorf("error querying for order number: %w", err)
	}
	return savedUserID, nil
}

// UpdateOrders обновление состояния списка заказов, которые были с незавершенными статусами
func (d *InDBRepo) UpdateOrders(ctx context.Context, tx pgx.Tx, updatedOrders []models.AccrualResponseData) error {
	const sqlQuery = `UPDATE orders SET status = $1, accrual=$2 WHERE order_number = $3`
	for _, order := range updatedOrders {
		_, err := tx.Exec(ctx, sqlQuery, order.Status, order.Accrual, order.Order)
		if err != nil {
			return err
		}
	}
	logrus.Infof("orders %v updated", updatedOrders)
	return nil
}

// GetProcessingOrders возвращение списка номеров заказов которые не имеют финального статуса
func (d *InDBRepo) GetProcessingOrders(ctx context.Context) ([]models.UserOrder, error) {
	const selectQuery = `SELECT order_number,status,uuid FROM orders WHERE status = $1 OR status = $2 OR status = $3`
	rows, err := d.dbPool.Query(ctx, selectQuery, "REGISTERED", "PROCESSING", "NEW")
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	defer rows.Close()
	var orders []models.UserOrder
	for rows.Next() {
		var order models.UserOrder
		if err = rows.Scan(&order.Number, &order.Status, &order.UserID); err != nil {
			logrus.Error(err)
			return nil, err
		}
		orders = append(orders, order)
	}
	if err = rows.Err(); err != nil {
		logrus.Error(err)
		return nil, err
	}
	return orders, nil
}

// GetUserProcessingOrders возвращение списка номеров заказов пользователя которые не имеют финального статуса
func (d *InDBRepo) GetUserProcessingOrders(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error) {
	const selectQuery = `SELECT order_number,status FROM orders WHERE uuid=$1 AND (status = $2 OR status = $3)`
	rows, err := d.dbPool.Query(ctx, selectQuery, userID, "NEW", "PROCESSING")
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	defer rows.Close()
	var orders []models.UserOrder
	for rows.Next() {
		var order models.UserOrder
		if err = rows.Scan(&order.Number, &order.Status); err != nil {
			logrus.Error(err)
			return nil, err
		}
		orders = append(orders, order)
	}
	if err = rows.Err(); err != nil {
		logrus.Error(err)
		return nil, err
	}
	logrus.Infof("return users processing orders %v", orders)
	return orders, nil
}

// TODO может условие с accrual nil в базе прописать, обратить внимание на совет Дениса

// GetUserOrders возвращает слайс всех заказов пользователя в формате models.UserOrder
func (d *InDBRepo) GetUserOrders(ctx context.Context, userID uuid.UUID) ([]models.UserOrder, error) {
	const selectQuery = `SELECT order_number,status,accrual,date FROM orders WHERE uuid = $1 ORDER BY date`
	rows, err := d.dbPool.Query(ctx, selectQuery, userID)
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	defer rows.Close()

	var userOrders []models.UserOrder

	for rows.Next() {
		var order models.UserOrder
		var accrualNull sql.NullString
		if err = rows.Scan(&order.Number, &order.Status, &accrualNull, &order.UploadedAt); err != nil {
			logrus.Error(err)
			return nil, err
		}
		if accrualNull.Valid {
			accrualValue, err := decimal.NewFromString(accrualNull.String)
			if err != nil {
				logrus.Error(err)
				return nil, err
			}
			order.Accrual = &accrualValue
		} else {
			order.Accrual = nil
		}
		userOrders = append(userOrders, order)
	}
	if err = rows.Err(); err != nil {
		logrus.Error(err)
		return nil, err
	}
	logrus.Infof("return processing orders %v", userOrders)
	return userOrders, nil
}

// StoreUserWithdrawal сохраняет в таблицу withdrawals новое списание баллов пользователя
func (d *InDBRepo) StoreUserWithdrawal(ctx context.Context, tx pgx.Tx, userID uuid.UUID, orderNumber string, sum decimal.Decimal) error {
	const sqlQuery = `INSERT INTO withdrawals (uuid,order_number,sum) VALUES ($1, $2,$3)`
	_, err := tx.Exec(ctx, sqlQuery, userID, orderNumber, sum)
	if err != nil {
		logrus.Error("new withdrawal don't save in database ", err)
		return err
	}
	return nil
}

// UpdateUserBalance обновляет состояние баланса у конкретного пользователя в таблице balance
func (d *InDBRepo) UpdateUserBalance(ctx context.Context, tx pgx.Tx, userID uuid.UUID, newBalance decimal.Decimal) error {
	const sqlQuery = `UPDATE balance SET user_balance = $1 WHERE uuid = $2`
	_, err := tx.Exec(ctx, sqlQuery, newBalance, userID)
	if err != nil {
		return err
	}
	return nil
}

// UsersBalanceUpdate обновляет состояние баланса пользователя
func (d *InDBRepo) UsersBalanceUpdate(ctx context.Context, tx pgx.Tx, usersBalanceToUpdate map[uuid.UUID]decimal.Decimal) error {
	const sqlQuery = `UPDATE balance SET user_balance = $1 WHERE uuid = $2`
	for userID, newBalance := range usersBalanceToUpdate {
		_, err := tx.Exec(ctx, sqlQuery, newBalance, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetUserBalance возвращает имеющийся на данный момент баланс пользователя
func (d *InDBRepo) GetUserBalance(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error) {
	const selectQuery = `SELECT user_balance FROM balance WHERE uuid = $1`
	var userBalance decimal.Decimal
	err := d.dbPool.QueryRow(ctx, selectQuery, userID).Scan(&userBalance)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logrus.Errorf("the userID not found: %s", err)
			return decimal.Zero, fmt.Errorf("the userID not found: %w", err)
		}
		logrus.Errorf("error querying for user_balance: %s", err)
		return decimal.Zero, fmt.Errorf("error querying for user_balance: %w", err)
	}
	return userBalance, nil
}

// GetUserWithdrawn возвращает сумму всех списаний пользователя
func (d *InDBRepo) GetUserWithdrawn(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error) {
	const selectQuery = `SELECT SUM(sum) FROM withdrawals WHERE uuid = $1`
	var userWithdrawn decimal.Decimal
	err := d.dbPool.QueryRow(ctx, selectQuery, userID).Scan(&userWithdrawn)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logrus.Errorf("the userID not found: %s", err)
			return decimal.Zero, fmt.Errorf("the uuid not found: %w", err)
		}
		logrus.Errorf("error querying for sum withdrawn: %s", err)
		return decimal.Zero, fmt.Errorf("error querying for sum withdrawn : %w", err)
	}
	return userWithdrawn, nil
}

// GetUserWithdrawals возвращает список всех списаний пользователя в порядке к более свежей дате
func (d *InDBRepo) GetUserWithdrawals(ctx context.Context, userID uuid.UUID) ([]models.UserWithdrawal, error) {
	const selectQuery = `SELECT order_number,sum,date FROM withdrawals WHERE uuid = $1 ORDER BY date`
	rows, err := d.dbPool.Query(ctx, selectQuery, userID)
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	defer rows.Close()

	var userWithdrawals []models.UserWithdrawal

	for rows.Next() {
		var withdrawal models.UserWithdrawal
		if err = rows.Scan(&withdrawal.Order, &withdrawal.Sum, &withdrawal.ProcessedAt); err != nil {
			logrus.Error(err)
			return nil, err
		}
		userWithdrawals = append(userWithdrawals, withdrawal)
	}
	if err = rows.Err(); err != nil {
		logrus.Error(err)
		return nil, err
	}
	logrus.Infof("return processing orders %v", userWithdrawals)
	return userWithdrawals, nil
}
