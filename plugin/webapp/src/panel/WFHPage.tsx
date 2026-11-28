// The Work from home page: request a day (or several), see your requests
// and their status, cancel one. Same rules as /att wfh — the bot checks.

import {FormError, cancelWFH, fetchWFH, requestWFH} from 'api';
import type {WFHItem, WFHState} from 'api';
import React, {useCallback, useEffect, useState} from 'react';

import {useRefresh} from './refresh';

const STATUS: Record<string, {label: string; key: string}> = {
    pending: {label: 'Pending', key: 'review'},
    approved: {label: 'Approved', key: 'progress'},
    rejected: {label: 'Rejected', key: 'blocked'},
    cancelled: {label: 'Cancelled', key: 'todo'},
};

function RequestForm({teamId, today, onSent, onCancel}: {teamId: string; today: string; onSent: (s: WFHState) => void; onCancel: () => void}) {
    const [start, setStart] = useState('');
    const [end, setEnd] = useState('');
    const [halfDay, setHalfDay] = useState(false);
    const [reason, setReason] = useState('');
    const [errors, setErrors] = useState<Record<string, string>>({});
    const [error, setError] = useState('');
    const [sending, setSending] = useState(false);
    const multiDay = Boolean(end && end !== start);

    const send = async (e: React.FormEvent) => {
        e.preventDefault();
        setSending(true);
        setErrors({});
        setError('');
        try {
            onSent(await requestWFH(teamId, {start_date: start, end_date: end, half_day: halfDay && !multiDay, reason: reason.trim()}));
        } catch (err) {
            if (err instanceof FormError && Object.keys(err.fields).length) {
                setErrors(err.fields);
            } else {
                setError((err as Error).message);
            }
        } finally {
            setSending(false);
        }
    };

    return (
        <form
            className='att-card att-card--plain att-form'
            onSubmit={send}
            aria-label='Request work from home'
        >
            <h3 className='att-h'>{'Request work from home'}</h3>
            <div className='att-two'>
                <label className='att-field'>
                    <span className='att-label'>{'From'}</span>
                    <input
                        className='att-input'
                        type='date'
                        min={today}
                        value={start}
                        onChange={(e) => setStart(e.target.value)}
                        required={true}
                        aria-invalid={Boolean(errors.start_date)}
                    />
                    {errors.start_date && <span className='att-field-error'>{errors.start_date}</span>}
                </label>
                <label className='att-field'>
                    <span className='att-label'>{'To (optional)'}</span>
                    <input
                        className='att-input'
                        type='date'
                        min={start || today}
                        value={end}
                        onChange={(e) => setEnd(e.target.value)}
                        aria-invalid={Boolean(errors.end_date)}
                    />
                    {errors.end_date && <span className='att-field-error'>{errors.end_date}</span>}
                </label>
            </div>
            <label className='att-check'>
                <input
                    type='checkbox'
                    checked={halfDay && !multiDay}
                    disabled={multiDay}
                    onChange={(e) => setHalfDay(e.target.checked)}
                />
                <span>{'Half day'}<span className='att-muted'>{' (for information; you still check in as usual)'}</span></span>
            </label>
            <label className='att-field'>
                <span className='att-label'>{'Reason'}</span>
                <textarea
                    className='att-input'
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                    maxLength={500}
                    rows={2}
                    required={true}
                    placeholder='e.g. Plumber visit in the morning'
                    aria-invalid={Boolean(errors.reason)}
                />
                {errors.reason && <span className='att-field-error'>{errors.reason}</span>}
            </label>
            {error && (
                <div
                    className='att-error'
                    role='alert'
                >{error}</div>
            )}
            <div className='att-actions att-actions--end'>
                <button
                    type='button'
                    className='att-btn'
                    onClick={onCancel}
                >{'Cancel'}</button>
                <button
                    type='submit'
                    className='att-btn att-btn--primary'
                    disabled={sending}
                >{sending ? 'Sending…' : 'Send request'}</button>
            </div>
        </form>
    );
}

function RequestRow({item, busy, onCancel}: {item: WFHItem; busy: boolean; onCancel: (id: number) => void}) {
    const [confirm, setConfirm] = useState(false);
    const st = STATUS[item.status] ?? {label: item.status, key: 'todo'};
    let decided = '';
    if (item.status === 'approved' && item.decided_by) {
        decided = `by @${item.decided_by}`;
    } else if (item.status === 'rejected') {
        decided = `by @${item.decided_by ?? ''}${item.note ? `: ${item.note}` : ''}`;
    }
    let action: React.ReactNode = null;
    if (item.can_cancel && confirm) {
        action = (
            <span className='att-confirm'>
                <button
                    className='att-btn att-btn--danger att-btn--small'
                    disabled={busy}
                    onClick={() => onCancel(item.id)}
                >{'Yes, cancel it'}</button>
                <button
                    className='att-link'
                    onClick={() => setConfirm(false)}
                >{'No'}</button>
            </span>
        );
    } else if (item.can_cancel) {
        action = (
            <button
                className='att-btn att-btn--small'
                disabled={busy}
                onClick={() => setConfirm(true)}
                aria-label={`Cancel request ${item.dates}`}
            >{'Cancel'}</button>
        );
    }
    return (
        <li className='att-task'>
            <div className='att-task-main'>
                <div className='att-wfh-dates'>{`${item.dates}${item.half_day ? ' · ½ day' : ''}`}</div>
                <div className='att-task-meta'>
                    <span className={`att-pill att-pill--${st.key}`}>{st.label}</span>
                    {decided && <span>{decided}</span>}
                </div>
                {item.reason && <div className='att-muted att-small'>{item.reason}</div>}
            </div>
            {action}
        </li>
    );
}

export default function WFHPage({teamId}: {teamId: string}) {
    const [data, setData] = useState<WFHState | null>(null);
    const [failed, setFailed] = useState('');
    const [form, setForm] = useState(false);
    const [notice, setNotice] = useState('');
    const [busy, setBusy] = useState(false);

    const load = useCallback(async () => {
        try {
            setData(await fetchWFH(teamId));
            setFailed('');
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [teamId]);

    useEffect(() => {
        setData(null);
        load();
    }, [load]);
    useRefresh(['wfh_changed'], load, !form);

    const cancel = async (id: number) => {
        setBusy(true);
        try {
            const r = await cancelWFH(teamId, id);
            setData(r.wfh);
            setNotice('Request cancelled.');
        } catch (e) {
            setFailed((e as Error).message);
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className='att-page-body'>
            {!form && (
                <button
                    className='att-btn att-btn--primary att-btn--wide'
                    onClick={() => {
                        setNotice('');
                        setForm(true);
                    }}
                >{'+ Request work from home'}</button>
            )}
            {form && data && (
                <RequestForm
                    teamId={teamId}
                    today={data.today}
                    onCancel={() => setForm(false)}
                    onSent={(s) => {
                        setData(s);
                        setForm(false);
                        setNotice('Request sent to the admins. You’ll get a DM when it’s decided.');
                    }}
                />
            )}
            {notice && (
                <div
                    className='att-notice'
                    role='status'
                >{`✅ ${notice}`}</div>
            )}
            {failed && (
                <div
                    className='att-error'
                    role='alert'
                >{failed}</div>
            )}
            <section
                className='att-card att-card--plain'
                aria-label='My requests'
            >
                <h3 className='att-h'>{'My requests'}</h3>
                {!data && !failed && <div className='att-empty'>{'Loading…'}</div>}
                {data && data.requests.length === 0 && <div className='att-empty'>{'No requests yet.'}</div>}
                {data && data.requests.length > 0 && (
                    <ul className='att-tasks'>
                        {data.requests.map((r) => (
                            <RequestRow
                                key={r.id}
                                item={r}
                                busy={busy}
                                onCancel={cancel}
                            />
                        ))}
                    </ul>
                )}
            </section>
        </div>
    );
}
