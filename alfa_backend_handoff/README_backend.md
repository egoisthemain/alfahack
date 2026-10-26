# Контракт модели для backend: JSON на входе и выходе

Этот комплект относится к реально сохранённой объединённой модели Alfa, feature schema `alpha-v1.1-unified-features-1`. Примеры input/output получены вызовом модели, а не придуманы для документации.

## Что передавать

Основной контракт — `input.example.json`: `request_id`, `application_id`, `feature_schema_version` и объект `features` с 32 именованными полями. Модель получает только 32 значения; остальные три поля — метаданные адаптера. Порядок ключей в JSON не важен: адаптер сам восстанавливает порядок из feature_contract.json. Нельзя отправлять массив без имён полей.

Эти 32 поля не нужно просить у клиента в анкете. Backend собирает сохранённые реестровые, финансовые, проверочные и БКИ-профили. Чтобы не переписывать формулы, используйте `source_snapshots.example.json` + `ScoringService.request_from_sources`: функция запускает тот же Feature Builder, что и обучение. Это вспомогательный вход Python-функции; отдельный HTTP endpoint в комплекте не реализован.

## Что возвращается

`output.example.json` содержит `pd` в диапазоне 0..1, горизонт 12 месяцев, `pd_grade`, версию схемы, отпечаток весов и режим калибровки. Например, 0.03 означает 3%. CatBoost возвращает PD, а адаптер добавляет класс A–E и метаданные. Одобрение, отказ, ставка, лимит и причины банковского решения рассчитываются отдельным Decision Engine.

Фактический результат основного примера: pd = 0.026779217258, класс A. `calibration_method` сейчас `raw`. `raw` здесь означает predict_proba без isotonic, а не логит. Несмотря на наличие файла калибратора, применять его без чтения default_inference_mode нельзя.

| Класс | PD за 12 месяцев |
|---|---|
| A | 0 ≤ PD < 0.04 |
| B | 0.04 ≤ PD < 0.08 |
| C | 0.08 ≤ PD < 0.15 |
| D | 0.15 ≤ PD < 0.25 |
| E | 0.25 ≤ PD ≤ 1 |

## Правила JSON

- Все 32 ключа features обязательны. Разрешённый пропуск передаётся явным `null`; пропуск ключа — ошибка.
- На границе JSON запрещены NaN/Infinity, строковые числа и числовые значения вместо категорий.
- Денежные значения — рубли, проценты и отношения — доли: 0.15, а не 15.
- `mass_address` и `no_reporting_12m` в готовых признаках — числовые 0/1. В исходных снимках используются boolean, Feature Builder преобразует их.
- Для IP `no_reporting_12m=null`. `employees=0` означает отсутствие наёмных работников и отличается от null.
- Если verified_annual_revenue известна, входной declared_annual_revenue должен быть null: это маскирование по V1.1. Исходную заявленную выручку сохраняйте в application для расчёта расхождения.
- Если подтверждённой выручки нет, передавайте заявленную, а annual_operating_profit, operating_margin и revenue_discrepancy_ratio — null.
- Для EXPRESS equipment_cost=null; для EQUIPMENT нужна положительная стоимость.
- Для фирмы моложе 24 месяцев revenue_growth_12m=null.
- ИНН, даты, company_id, loans, будущие поля и default_12m не входят в features. В исходных снимках ИНН/даты/loans нужны Feature Builder для связи и проверок.

## Все входные признаки

| Поле | Тип JSON | Значение |
|---|---|---|
| `product` | string | Продукт кредита |
| `legal_form` | string | Правовая форма |
| `industry_group` | string | Укрупнённая отрасль, вычисленная из ОКВЭД |
| `msp_category` | string | Категория масштаба МСП из сохранённого профиля |
| `amount` | integer | Сумма запрашиваемого кредита, рубли |
| `term_months` | integer | Срок запрашиваемого кредита, полные месяцы |
| `declared_annual_revenue` | number / null | Заявленная выручка используется только при отсутствии подтверждённой; иначе null |
| `equipment_cost` | number / null | Стоимость оборудования, рубли; EXPRESS -> null |
| `business_age_months` | integer | Полные месяцы между регистрацией и заявкой; SF03 проверяется до вызова модели |
| `mass_address` | integer | Массовый адрес: числовой флаг 0/1 |
| `no_reporting_12m` | integer / null | 0/1; для IP неприменимо и передаётся null |
| `employees` | integer / null | Численность наёмных работников; IP может иметь 0; null означает неизвестно |
| `enforcement_debt_rub` | number / null | Исполнительный долг, рубли; null означает неизвестно |
| `rating` | integer / null | Кредитный рейтинг условной шкалы 1–999; null при отсутствии |
| `existing_monthly_payment_rub` | number | Сумма месячных платежей по старым кредитам, рубли |
| `max_overdue_days_12m` | integer | Максимальная историческая просрочка за 12 месяцев, дни |
| `outstanding_debt_rub` | number | Общий остаток долга по старым кредитам, рубли |
| `active_loans_count` | integer | Количество действующих обязательств |
| `verified_annual_revenue` | number / null | Подтверждённая выручка за 12 месяцев, рубли; null при отсутствии |
| `annual_operating_profit` | number / null | Подтверждённая выручка минус операционные расходы, рубли |
| `operating_margin` | number / null | Операционная прибыль / подтверждённая выручка, доля; 0.15 означает 15% |
| `cashflow_available_for_debt_12m` | number | Денежный поток до долговых платежей за 12 месяцев, рубли; может быть отрицательным |
| `cash_balance_rub` | number | Денежный остаток, рубли |
| `revenue_growth_12m` | number / null | Выручка последних 12 мес. / предыдущих 12 мес. - 1; null, если история неполная |
| `revenue_volatility_12m` | number | Стандартное отклонение месячной выручки / средняя месячная выручка |
| `negative_cashflow_months_12m` | integer | Количество отрицательных по CFADS месяцев за 12 месяцев |
| `revenue_discrepancy_ratio` | number / null | (Заявленная - подтверждённая выручка) / подтверждённая; null, если её нет |
| `monthly_revenue` | number / null | Выбранная годовая выручка / 12; null при недоступной или нулевой базе |
| `new_payment_at_reference_rate` | number | Аннуитет по amount, сроку и фиксированным 25% годовых; рубли в месяц |
| `payment_to_revenue` | number / null | (Старые платежи + новый платёж) / месячная выручка; доля |
| `loan_amount_to_revenue` | number / null | Сумма нового кредита / выбранная годовая выручка |
| `projected_dscr` | number | (CFADS за 12 мес. / 12) / (старые платежи + новый платёж); может быть отрицательным |

## Где запускать проверки

Перед прямым запросом с готовыми features backend обязан проверить согласие БКИ, полноту источников, стоп-факторы SF01–SF06 и временную доступность данных. В 32 признаках нет всех сведений для этих проверок. `request_from_sources` использует существующие проверки Feature Builder: даты срезов, возраст, часть реестровых стоп-факторов, просрочка, запрещённые отрасли, согласие. Полную продуктовую политику реализует backend.

`predict_safe()` возвращает `(payload, 200)` при успехе или `(error_payload, 422)` при ошибке входа. HTTP-коды — готовое соглашение для вашего маршрута; сам HTTP сервер сюда не добавлен. Некорректный запрос не превращается в PD=1 или отказ по кредиту. Сбой загрузки модели при старте должен считаться ошибкой сервиса, а не ответом скоринга.

## Подключение

```sh
pip install -r requirements.txt
python scoring_adapter.py --input input.example.json
python scoring_adapter.py --input source_snapshots.example.json --sources
```

```python
from scoring_adapter import ScoringService

scorer = ScoringService()  # один раз при старте приложения
payload, http_status = scorer.predict_safe(request_json)

# Если backend отдаёт исходные снимки вместо готового X:
request_json = scorer.request_from_sources(source_snapshots, request_id="my-request-1")
payload, http_status = scorer.predict_safe(request_json)
```

Модуль не обращается к CSV обучающей выборки и не запускает генератор. Сохранённые веса загружаются один раз при создании ScoringService; для каждого запроса считаются только признаки и predict_proba.

## Содержимое

- `input.example.json`, `output.example.json` — реальная пара запрос/ответ для ООО.
- `input.ip_missing_financials.example.json`, `output.ip_missing_financials.example.json` — ИП с отсутствующей подтверждённой выручкой и неприменимой отчётностью.
- `source_snapshots.example.json` — пять связанных исходных блоков на одну дату.
- `request.schema.json`, `response.schema.json` — JSON Schema Draft 2020-12, можно валидировать независимо от Python.
- `error.example.json` — ошибка при отсутствии обязательного признака.
- `scoring_adapter.py`, `feature_builder.py` — JSON-адаптер и прежний единый расчёт признаков.
- `models/` — необходимые веса, калибратор, список признаков и метаданные.
- `verification.json` — результаты проверок входа, null, порядка ключей, дат и повторения исходного прогноза.

Модель обучена на синтетических контрфактических исходах. Поле training_data_type позволяет сохранить эту информацию в ответе сервиса.
