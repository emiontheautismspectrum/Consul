Техническое задание (ТЗ): ИИ-финансовый консультант для МСБ

Название проекта: FinAdvisor AI (рабочее название)
Цель: Создание веб-приложения для повышения финансовой грамотности предпринимателей МСБ, предоставляющего инструменты генерации бизнес-планов с помощью ИИ и расчета кредитных нагрузок.
1. Архитектура системы (Модульный монолит)

Система проектируется как единый развертываемый сервис (монолит), внутренне разделенный на строго изолированные доменные модули. Взаимодействие между модулями происходит через интерфейсы (Go interfaces), а не сетевые вызовы.

Структура проекта (Go):

    cmd/api/ — точка входа, инициализация сервера, подключение к БД и Redis.

    internal/

        auth/ — логика регистрации, логина, генерации и валидации JWT, хеширования.

        advisor/ — логика ИИ-консультанта, промпты, история генерации бизнес-планов.

        calculator/ — кредитный калькулятор, математика аннуитетных и дифференцированных платежей.

        infrastructure/ — общий код: подключение к БД, клиенты ИИ (OpenAI/Anthropic API), HTTP-middleware.

2. Технологический стек

    Backend: Go (чистый net/http с использованием стандартного *http.ServeMux из Go 1.22+).

    База данных: PostgreSQL + PgBouncer (Transaction mode) для пулинга и лимитирования коннектов.

    Кэш и сессии: Redis (хранение blacklist токенов, кэширование расчетов калькулятора, rate-limit счетчики, если не хватит NGINX).

    Frontend: Next.js (App Router, React Server Components для SSR и SEO).

    Веб-сервер / Proxy: NGINX (Reverse proxy, SSL терминация, Rate Limiting).

3. База данных и работа с данными (Без ORM)

Взаимодействие с базой данных реализуется через стандартный пакет database/sql и драйвер (например, pgx/stdlib). Строго запрещена конкатенация строк при формировании SQL-запросов — используются только параметризованные запросы ($1, $2 и т.д.).

Пул соединений: Приложение подключается не напрямую к PostgreSQL, а к порту PgBouncer, который лимитирует количество реальных транзакций к БД, предотвращая исчерпание ресурсов.
Основные таблицы и индексы

    Таблица users

        id (UUID), email (VARCHAR, UNIQUE), password_hash (VARCHAR), salt (VARCHAR).

        Индекс: CREATE UNIQUE INDEX idx_users_email ON users (email);

    Таблица business_plans

        id (UUID), user_id (UUID), title (VARCHAR), content (TEXT), created_at (TIMESTAMP).

        Индекс: CREATE INDEX idx_bplans_user_created ON business_plans (user_id, created_at DESC); (для быстрой сортировки историй пользователя).

    Таблица loan_calculations (история расчетов)

        id, user_id, amount, rate, term, result_jsonb.

Пример SQL с использованием JOIN, FILTER и LIMIT:

Для получения агрегированной статистики пользователя и последних бизнес-планов:
SQL

SELECT 
    u.email,
    COUNT(bp.id) AS total_plans,
    COUNT(lc.id) FILTER (WHERE lc.amount > 1000000) AS large_loans_calculated,
    bp.title AS latest_plan_title
FROM users u
LEFT JOIN business_plans bp ON u.id = bp.user_id
LEFT JOIN loan_calculations lc ON u.id = lc.user_id
WHERE u.id = $1
GROUP BY u.email, bp.title, bp.created_at
ORDER BY bp.created_at DESC
LIMIT 5 OFFSET $2;

4. Авторизация и Безопасность

    Хеширование паролей: Используется алгоритм Argon2id или bcrypt.

        Salt: Уникальная для каждого пользователя строка, генерируется при регистрации и хранится в БД.

        Pepper: Секретная строка, хранится только в переменных окружения (ENV) бэкенда, в БД не попадает.

        Формула: hash = HASH(password + salt + pepper). В БД хранится только hash и salt.

    JWT Токены:

        access_token (время жизни ~15 минут).

        refresh_token (время жизни ~7-30 дней).

        Оба токена передаются клиенту исключительно через Set-Cookie с флагами HttpOnly, Secure, SameSite=Strict для защиты от XSS и CSRF.

    Ротация токенов: При истечении access_token Next.js делает запрос на /api/auth/refresh, бэкенд проверяет валидность refresh_token (и проверяет, что он не в Redis-блэклисте) и выдает новую пару токенов.

5. API и Маршрутизация (Go net/http)

API строится на REST-архитектуре с использованием middleware для логирования, проверки JWT и CORS.

Ключевые эндпоинты:

    Auth:

        POST /api/v1/auth/register

        POST /api/v1/auth/login

        POST /api/v1/auth/refresh

        POST /api/v1/auth/logout (удаляет куки, добавляет refresh токен в Redis blacklist).

    ИИ-Консультант (Бизнес-планы):

        POST /api/v1/advisor/generate — принимает вводные данные (ниша, бюджет), делает запрос к LLM (например, OpenAI API), сохраняет результат в PostgreSQL.

        GET /api/v1/advisor/plans?limit=10&offset=0 — получение истории (с пагинацией).

    Кредитный калькулятор:

        POST /api/v1/calculator/calculate — расчет аннуитетных/дифференцированных платежей (выполняется на Go для скорости и точности float-арифметики).

        GET /api/v1/calculator/history

6. Конфигурация NGINX (Проксирование и Rate Limit)

NGINX выступает единой точкой входа (API Gateway). Он маршрутизирует статику на Next.js, а API-запросы — на Go-монолит, попутно защищая от DDoS.

Пример конфигурации:
Nginx

# Настройка зоны для рейт-лимита (10 запросов в секунду с одного IP)
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=10r/s;

server {
    listen 443 ssl http2;
    server_name finadvisor.uz;

    # SSL сертификаты (Let's Encrypt)
    ssl_certificate /etc/letsencrypt/live/.../fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/.../privkey.pem;

    # Роутинг на Frontend (Next.js)
    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # Роутинг на Backend (Go) с Rate Limiting
    location /api/ {
        limit_req zone=api_limit burst=20 nodelay;
        
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}

7. Требования к Frontend (Next.js)

    Управление состоянием: React Context или Zustand для хранения состояния UI, так как токены лежат в HttpOnly кукисах (клиентский JS к ним доступа не имеет).

    Архитектура: Использование Server Actions для мутаций данных (создание бизнес-плана) и Server Components для первичного рендера дашборда с историей расчетов.

    Дизайн: Чистый интерфейс с формами пошагового ввода (Wizard) для сбора данных о бизнесе перед генерацией ИИ-советов.
