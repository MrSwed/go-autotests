package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/suite"

	"github.com/Yandex-Practicum/go-autotests/internal/fork"
)

// Iteration5Suite является сьютом с тестами и состоянием для инкремента
type Iteration5Suite struct {
	suite.Suite

	serverAddress string
	serverBaseURL string
	serverProcess *fork.BackgroundProcess
}

// SetupSuite подготавливает необходимые зависимости
func (suite *Iteration5Suite) SetupSuite() {
	// проверяем наличие необходимых флагов
	suite.Require().NotEmpty(flagTargetBinaryPath, "-binary-path non-empty flag required")
	suite.Require().NotEmpty(flagServerPort, "-server-port non-empty flag required")

	// запускаем процесс тестируемого сервера
	{
		suite.serverAddress = "localhost:" + flagServerPort
		suite.serverBaseURL = "http://" + suite.serverAddress
		envs := append(os.Environ(), []string{
			"SERVER_ADDRESS=" + suite.serverAddress,
			"BASE_URL=" + suite.serverBaseURL,
		}...)

		p := fork.NewBackgroundProcess(context.Background(), flagTargetBinaryPath,
			fork.WithEnv(envs...),
		)
		suite.serverProcess = p

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		err := p.Start(ctx)
		if err != nil {
			suite.T().Errorf("Невозможно запустить процесс командой %s: %s. Переменные окружения: %+v", p, err, envs)
			return
		}

		err = p.WaitPort(ctx, "tcp", flagServerPort)
		if err != nil {
			suite.T().Errorf("Не удалось дождаться пока порт %s станет доступен для запроса: %s", flagServerPort, err)
			return
		}
	}
}

// TearDownSuite высвобождает имеющиеся зависимости
func (suite *Iteration5Suite) TearDownSuite() {
	exitCode, err := suite.serverProcess.Stop(syscall.SIGINT, syscall.SIGKILL)
	if err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return
		}
		suite.T().Logf("Не удалось остановить процесс с помощью сигнала ОС: %s", err)
		return
	}

	if exitCode > 0 {
		suite.T().Logf("Процесс завершился с не нулевым статусом %d", exitCode)
	}

	// try to read stdout/stderr
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	out := suite.serverProcess.Stderr(ctx)
	if len(out) > 0 {
		suite.T().Logf("Получен STDERR лог процесса:\n\n%s", string(out))
	}
	out = suite.serverProcess.Stdout(ctx)
	if len(out) > 0 {
		suite.T().Logf("Получен STDOUT лог процесса:\n\n%s", string(out))
	}
}

// TestEnvVars attempts to:
// - generate and send random URL to shorten handler
// - generate and send random URL to shorten API handler
// - fetch original URLs by sending shorten URLs to expand handler one by one
func (suite *Iteration5Suite) TestEnvVars() {
	shortenURLs := make(map[string]string)

	// create HTTP client without redirects support and custom resolver
	errRedirectBlocked := errors.New("HTTP redirect blocked")
	redirPolicy := resty.RedirectPolicyFunc(func(_ *http.Request, _ []*http.Request) error {
		return errRedirectBlocked
	})

	restyClient := resty.New()
	transport := restyClient.GetClient().Transport.(*http.Transport)

	// mock all network requests to be resolved at localhost
	resolveIP := "127.0.0.1:" + flagServerPort
	transport.DialContext = mockResolver("tcp", suite.serverAddress, resolveIP)

	httpc := restyClient.
		SetTransport(transport).
		SetHostURL(suite.serverBaseURL).
		SetRedirectPolicy(redirPolicy)

	suite.Run("shorten", func() {
		originalURL := generateTestURL(suite.T())

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req := httpc.R().
			SetContext(ctx).
			SetBody(originalURL)
		resp, err := req.Post("/")

		noRespErr := suite.Assert().NoError(err, "Ошибка при попытке сделать запрос для сокращения URL")

		shortenURL := string(resp.Body())

		validStatus := suite.Assert().Equalf(http.StatusCreated, resp.StatusCode(),
			"Несоответствие статус кода ответа ожидаемому в хендлере '%s %s'", req.Method, req.URL)

		_, urlParseErr := url.Parse(shortenURL)
		validURL := suite.Assert().NoErrorf(urlParseErr,
			"Невозможно распарсить полученный сокращенный URL - %s : %s", shortenURL, err,
		)

		if !noRespErr || !validStatus || !validURL {
			dump := dumpRequest(req.RawRequest, true)
			suite.T().Logf("Оригинальный запрос:\n\n%s", dump)
		}

		shortenURLs[originalURL] = shortenURL
	})

	suite.Run("shorten_api", func() {
		originalURL := generateTestURL(suite.T())

		type shortenRequest struct {
			URL string `json:"url"`
		}

		type shortenResponse struct {
			Result string `json:"result"`
		}

		var result shortenResponse

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req := httpc.R().
			SetContext(ctx).
			SetHeader("Content-Type", "application/json").
			SetBody(&shortenRequest{
				URL: originalURL,
			}).
			SetResult(&result)
		resp, err := req.Post("/api/shorten")

		noRespErr := suite.Assert().NoError(err, "Ошибка при попытке сделать запрос для сокращения URL")

		shortenURL := result.Result

		validStatus := suite.Assert().Equalf(http.StatusCreated, resp.StatusCode(),
			"Несоответствие статус кода ответа ожидаемому в хендлере '%s %s'", req.Method, req.URL)

		validContentType := suite.Assert().Containsf(resp.Header().Get("Content-Type"), "application/json",
			"Заголовок ответа Content-Type содержит несоответствующее значение",
		)

		_, urlParseErr := url.Parse(shortenURL)
		validURL := suite.Assert().NoErrorf(urlParseErr,
			"Невозможно распарсить полученный сокращенный URL - %s : %s", shortenURL, err,
		)

		if !noRespErr || !validStatus || !validContentType || !validURL {
			dump := dumpRequest(req.RawRequest, true)
			suite.T().Logf("Оригинальный запрос:\n\n%s", dump)
		}

		shortenURLs[originalURL] = shortenURL
	})

	suite.Run("expand", func() {
		for originalURL, shortenURL := range shortenURLs {
			req := resty.New().
				SetRedirectPolicy(redirPolicy).
				R()
			resp, err := req.Get(shortenURL)
			noRespErr := true
			if !errors.Is(err, errRedirectBlocked) {
				noRespErr = suite.Assert().NoErrorf(err, "Ошибка при попытке сделать запрос для получения исходного URL")
			}

			validStatus := suite.Assert().Equalf(http.StatusTemporaryRedirect, resp.StatusCode(),
				"Несоответствие статус кода ответа ожидаемому в хендлере '%s %s'", req.Method, req.URL,
			)
			validURL := suite.Assert().Equalf(originalURL, resp.Header().Get("Location"),
				"Несоответствие URL полученного в заголовке Location ожидаемому",
			)

			if !noRespErr || !validStatus || !validURL {
				dump := dumpRequest(req.RawRequest, true)
				suite.T().Logf("Оригинальный запрос:\n\n%s", dump)
			}
		}
	})
}
