INSERT INTO companies (inn, name, legal_form, registration_date, status, okved)
VALUES ('123', 'OOO Тест-Ромашка', 'OOO', '2022-10-01', 'ACTIVE', '47.11')
RETURNING company_id, inn, name;

