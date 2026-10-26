"""Generated from the executed notebook. One Feature Builder for training and inference."""
from pathlib import Path
import json
import numpy as np
import pandas as pd
PROFILE = {"future": {"reference_rate": 0.25}}
CATEGORICAL_FEATURES=['product','legal_form','industry_group','msp_category']

NUMERIC_FEATURES=['amount','term_months','declared_annual_revenue','equipment_cost','business_age_months',
 'mass_address','no_reporting_12m','employees','enforcement_debt_rub','rating',
 'existing_monthly_payment_rub','max_overdue_days_12m','outstanding_debt_rub','active_loans_count',
 'verified_annual_revenue','annual_operating_profit','operating_margin','cashflow_available_for_debt_12m',
 'cash_balance_rub','revenue_growth_12m','revenue_volatility_12m','negative_cashflow_months_12m',
 'revenue_discrepancy_ratio','monthly_revenue','new_payment_at_reference_rate','payment_to_revenue',
 'loan_amount_to_revenue','projected_dscr']

FEATURE_COLUMNS=CATEGORICAL_FEATURES+NUMERIC_FEATURES

BANNED={'inn','company_id','application_id','default_12m','generator_scenario','generator_profile','business_scale','health',
 'latent_default_probability','first_default_month','performance_observed_until','future_revenue','future_cfads',
 'current_overdue_days','director_disqualified','bankruptcy','status'}

def annuity(amount, annual_rate, term_months):
    a,r,t=np.asarray(amount,float),np.asarray(annual_rate,float)/12,np.asarray(term_months,float)
    return np.where(r==0,a/t,a*r/(1-(1+r)**(-t)))

def make_features(tables, applications=None, require_eligible=True):
    a=(tables['applications'] if applications is None else applications).copy()
    a=a.rename(columns={'status':'application_status'})
    c=tables['companies'].rename(columns={'status':'registry_status'})
    d=a.merge(c.drop(columns='inn'),on='company_id',how='left',validate='many_to_one')
    for table in ['financial_profiles','business_checks','bki_profiles']:
        source=tables[table]
        if source.company_id.duplicated().any():raise ValueError(f'{table}: нужен выбор исторического среза до заявки')
        d=d.merge(source,on='company_id',how='left',validate='many_to_one')
    if d[['company_id','registration_date','bki_as_of_date','financial_as_of_date','check_as_of_date']].isna().any().any():
        raise ValueError('Не хватает обязательных исходных срезов')
    submitted=pd.to_datetime(d.submitted_at,utc=True)
    for col in ['bki_as_of_date','financial_as_of_date','check_as_of_date']:
        if (pd.to_datetime(d[col],utc=True)>submitted).any():raise ValueError(f'Будущий срез: {col}')
    registration=pd.to_datetime(d.registration_date,utc=True)
    d['business_age_months']=(submitted.dt.year-registration.dt.year)*12+(submitted.dt.month-registration.dt.month)-(submitted.dt.day<registration.dt.day).astype(int)
    if (d['business_age_months']<0).any():raise ValueError('Заявка раньше регистрации')
    if require_eligible:
        blocked=(d.registry_status!='ACTIVE')|d.bankruptcy|d.director_disqualified|(d.current_overdue_days>30)|(d.business_age_months<3)
        prohibited=d.okved.astype(str).str.match(r'^(92[.]|12[.]|11[.]0[1-5])')
        if (blocked|prohibited).any():raise ValueError('Стоп-фактор: модель не вызывается')
        if not d.bki_consent.all():raise ValueError('Нет согласия на БКИ')
    selected=d.verified_annual_revenue.where(d.verified_annual_revenue.notna(),d.declared_annual_revenue).astype(float)
    selected=selected.where(selected>0)
    d['monthly_revenue']=selected/12
    d['new_payment_at_reference_rate']=annuity(d.amount,PROFILE['future']['reference_rate'],d.term_months)
    total=d.existing_monthly_payment_rub+d.new_payment_at_reference_rate
    d['payment_to_revenue']=total/d.monthly_revenue
    d['loan_amount_to_revenue']=d.amount/selected
    d['projected_dscr']=(d.cashflow_available_for_debt_12m/12)/total.where(total>0)
    d['revenue_discrepancy_ratio']=(d.declared_annual_revenue-d.verified_annual_revenue)/d.verified_annual_revenue.where(d.verified_annual_revenue>0)
    x=d[FEATURE_COLUMNS].copy()
    x.loc[d.verified_annual_revenue.notna(),'declared_annual_revenue']=np.nan
    for col in CATEGORICAL_FEATURES:x[col]=x[col].fillna('__UNKNOWN__').astype(str)
    for col in NUMERIC_FEATURES:x[col]=pd.to_numeric(x[col],errors='raise').astype(float)
    if np.isinf(x[NUMERIC_FEATURES].to_numpy()).any():raise ValueError('Бесконечность в X')
    if BANNED.intersection(x.columns):raise ValueError('Утечка в X')
    return x,d[['application_id','company_id']]

def read_tables(folder):
    result={}
    for name in ['companies','business_checks','financial_profiles','bki_profiles','applications']:
        frame=pd.read_csv(Path(folder)/f'{name}.csv',dtype={'inn':str,'okved':str})
        for col in ['loans','founders']:
            if col in frame:frame[col]=frame[col].map(json.loads)
        for col in ['mass_address','director_disqualified','no_reporting_12m','bankruptcy','bki_consent']:
            if col in frame:
                frame[col]=frame[col].astype('boolean')
        result[name]=frame
    return result

def grade_from_pd(pd_value):return 'ABCDE'[int(np.searchsorted([.04,.08,.15,.25],pd_value,side='right'))]
