// Package api — HTTP-интерфейс сервиса telegram-api для соседей.
//
// Наружу в интернет этот API не выставляется: им пользуется scheduler,
// чтобы разослать вопросы. Внутрикластерный ClusterIP-сервис.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/Ivanvnew75/libs/common"

	"github.com/Ivanvnew75/telegram-api/telegram"
	"github.com/Ivanvnew75/telegram-api/usersclient"
)

type Server struct {
	tg    *telegram.Client
	users *usersclient.Client
	log   *slog.Logger
}

func New(tg *telegram.Client, users *usersclient.Client, log *slog.Logger) *Server {
	return &Server{tg: tg, users: users, log: log}
}

func (s *Server) Echo() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Тот же набор и тот же порядок middleware, что в остальных сервисах:
	// формат лога обязан совпадать, иначе искать по всем сервисам сразу
	// не получится.
	m := common.NewMetrics("telegram-api")
	e.Use(common.RequestID())
	e.Use(common.PropagateRequestID())
	e.Use(m.Middleware())
	e.Use(common.RequestLogger(s.log))
	e.Use(middleware.Recover())
	m.Register(e)

	e.GET("/health", s.health)
	e.GET("/ready", s.ready)
	e.POST("/send", s.send)
	return e
}

// health — liveness. Только про процесс, без внешних вызовов.
func (s *Server) health(c echo.Context) error {
	return c.JSON(http.StatusOK, echo.Map{"status": "ok"})
}

// ready — readiness. Проверяет обе зависимости: Telegram и сервис users.
//
// Почему проверяются обе: без Telegram сервис не сможет отправить
// сообщение, без users — не поймёт, кому. В любом из этих состояний
// слать сюда трафик бессмысленно, и честнее сказать 503, чем принять
// запрос и провалить его.
func (s *Server) ready(c echo.Context) error {
	ctx := c.Request().Context()

	if _, err := s.tg.GetMe(ctx); err != nil {
		return c.JSON(http.StatusServiceUnavailable, echo.Map{
			"status": "unavailable", "reason": "telegram api unreachable",
		})
	}
	if err := s.users.Health(ctx); err != nil {
		return c.JSON(http.StatusServiceUnavailable, echo.Map{
			"status": "unavailable", "reason": "users service unreachable",
		})
	}
	return c.JSON(http.StatusOK, echo.Map{"status": "ready"})
}

type sendRequest struct {
	TelegramID int64  `json:"telegram_id"`
	Text       string `json:"text"`
}

// send — POST /send. Отправить сообщение пользователю.
func (s *Server) send(c echo.Context) error {
	var in sendRequest
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "invalid request body"})
	}
	if in.TelegramID == 0 || in.Text == "" {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "telegram_id and text are required"})
	}

	// Свой контекст с таймаутом: если клиент отвалился, отправку
	// всё равно доводим до конца — сообщение либо ушло, либо нет,
	// промежуточного состояния быть не должно.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 10*time.Second)
	defer cancel()

	if err := s.tg.SendMessage(ctx, in.TelegramID, in.Text); err != nil {
		s.log.Error("send failed",
			slog.Int64("telegram_id", in.TelegramID),
			slog.String("error", err.Error()))
		// 502 Bad Gateway, а не 500: ошибка не в нас, а в вышестоящем
		// сервисе. Различие важно для алертов — 502 указывает наружу.
		return c.JSON(http.StatusBadGateway, echo.Map{"error": "telegram api error"})
	}

	s.log.Info("message sent", slog.Int64("telegram_id", in.TelegramID))
	return c.JSON(http.StatusOK, echo.Map{"status": "sent"})
}
