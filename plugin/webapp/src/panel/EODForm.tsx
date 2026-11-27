// The end-of-day report form, inside the panel. Same content and rules as
// the dialog (the bot builds it and checks the submission): a status per
// active task, tasks already closed today, other tasks finished, and three
// text fields.

import {EODSubmitError, submitEOD} from 'api';
import type {EODForm as Form} from 'api';
import React, {useState} from 'react';

type Props = {
    teamId: string;
    form: Form;
    onDone: (saved: Form) => void;
    onCancel: () => void;
};

const STATUS_KEY: Record<string, string> = {
    'In progress': 'progress',
    'In review': 'review',
    Blocked: 'blocked',
    Done: 'done',
};

function Counter({value, max}: {value: string; max: number}) {
    const n = [...value].length;
    return <span className={`att-counter${n > max ? ' att-counter--over' : ''}`}>{`${n}/${max}`}</span>;
}

export default function EODForm({teamId, form, onDone, onCancel}: Props) {
    const [choices, setChoices] = useState<Record<string, string>>(() =>
        Object.fromEntries(form.tasks.map((t, i) => [`s${i}`, t.choice || form.no_change])));
    const [others, setOthers] = useState<Set<string>>(() =>
        new Set(form.others.map((t, i) => (t.checked ? String(i) : '')).filter(Boolean)));
    const [did, setDid] = useState(form.did);
    const [blockers, setBlockers] = useState(form.blockers);
    const [tomorrow, setTomorrow] = useState(form.tomorrow);
    const [errors, setErrors] = useState<Record<string, string>>({});
    const [error, setError] = useState('');
    const [sending, setSending] = useState(false);

    const options = [form.no_change, ...form.statuses];
    let submitLabel = form.submitted ? 'Update report' : 'Send report';
    if (sending) {
        submitLabel = 'Sending…';
    }

    const toggleOther = (i: string) => {
        const next = new Set(others);
        if (next.has(i)) {
            next.delete(i);
        } else {
            next.add(i);
        }
        setOthers(next);
    };

    const send = async (e: React.FormEvent) => {
        e.preventDefault();
        if ([...did.trim()].length < form.min_did) {
            setErrors({did: `Write at least ${form.min_did} characters.`});
            return;
        }
        setSending(true);
        setErrors({});
        setError('');
        try {
            const saved = await submitEOD(teamId, form.state, {
                choices, others: [...others], did: did.trim(), blockers: blockers.trim(), tomorrow: tomorrow.trim(),
            });
            onDone(saved);
        } catch (err) {
            if (err instanceof EODSubmitError && Object.keys(err.fields).length) {
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
            className='att-eod'
            onSubmit={send}
            aria-label='End-of-day report'
        >
            <div className='att-eod-head'>
                <button
                    type='button'
                    className='att-link'
                    onClick={onCancel}
                >{'← Back'}</button>
                <h3 className='att-h'>{`End-of-day report · ${form.day_label}`}</h3>
                {form.submitted && <div className='att-muted att-small'>{'You already sent this report: sending again updates it.'}</div>}
            </div>

            {form.note && <div className='att-empty'>{form.note}</div>}

            {form.tasks.length > 0 && (
                <fieldset className='att-fieldset'>
                    <legend className='att-group-h'>{'Your tasks: what changed today?'}</legend>
                    {form.tasks.map((t, i) => {
                        const name = `s${i}`;
                        return (
                            <div
                                className='att-eod-task'
                                key={t.url}
                            >
                                {/* Mattermost's ExternalLink component isn't available to plugins; rel=noopener noreferrer keeps the new tab safe. */}
                                {/* eslint-disable-next-line @mattermost/use-external-link */}
                                <a
                                    className='att-task-title'
                                    href={t.url}
                                    target='_blank'
                                    rel='noopener noreferrer'
                                >{t.title}</a>
                                <div className='att-task-meta'>
                                    <span>{`${t.repo.split('/').pop()} #${t.number}`}</span>
                                    <span>{`now: ${t.status || 'No status'}`}</span>
                                </div>
                                <div
                                    className='att-seg'
                                    role='radiogroup'
                                    aria-label={`New status for #${t.number} ${t.title}`}
                                >
                                    {options.map((o) => (
                                        <button
                                            type='button'
                                            key={o}
                                            role='radio'
                                            aria-checked={choices[name] === o}
                                            className={`att-seg-btn att-seg-btn--${STATUS_KEY[o] ?? 'none'}${choices[name] === o ? ' is-on' : ''}`}
                                            onClick={() => setChoices({...choices, [name]: o})}
                                        >{o}</button>
                                    ))}
                                </div>
                                {errors[name] && <div className='att-field-error'>{errors[name]}</div>}
                            </div>
                        );
                    })}
                    {Boolean(form.omitted) && <div className='att-muted att-small'>{`${form.omitted} more active tasks aren’t shown.`}</div>}
                </fieldset>
            )}

            {form.done.length > 0 && (
                <div className='att-fieldset'>
                    <div className='att-group-h'>{'Already done today'}</div>
                    <ul className='att-plain-list'>
                        {form.done.map((t) => (
                            <li key={t.url}>{`✅ ${t.title} `}<span className='att-muted'>{`#${t.number}`}</span></li>
                        ))}
                    </ul>
                </div>
            )}

            {form.others.length > 0 && (
                <fieldset className='att-fieldset'>
                    <legend className='att-group-h'>{'Other tasks I finished'}</legend>
                    {form.others.map((t, i) => (
                        <label
                            className='att-check'
                            key={t.url}
                        >
                            <input
                                type='checkbox'
                                checked={others.has(String(i))}
                                onChange={() => toggleOther(String(i))}
                            />
                            <span>
                                {t.title}
                                <span className='att-muted'>{` #${t.number}`}</span>
                            </span>
                        </label>
                    ))}
                    {errors.others && <div className='att-field-error'>{errors.others}</div>}
                </fieldset>
            )}

            <label className='att-field'>
                <span className='att-label'>
                    {'What I did today'}
                    <span
                        className='att-required'
                        aria-hidden='true'
                    >{' *'}</span>
                    <Counter
                        value={did}
                        max={form.max_text}
                    />
                </span>
                <textarea
                    className='att-input'
                    value={did}
                    onChange={(e) => setDid(e.target.value)}
                    maxLength={form.max_text}
                    required={true}
                    rows={4}
                    placeholder='e.g. Finished session timeout handling, reviewed the CSV export'
                    aria-invalid={Boolean(errors.did)}
                />
                {errors.did && <span className='att-field-error'>{errors.did}</span>}
            </label>
            <label className='att-field'>
                <span className='att-label'>
                    {'Blockers'}
                    <Counter
                        value={blockers}
                        max={form.max_text}
                    />
                </span>
                <textarea
                    className='att-input'
                    value={blockers}
                    onChange={(e) => setBlockers(e.target.value)}
                    maxLength={form.max_text}
                    rows={2}
                    placeholder='Anything stopping you? Admins get an alert.'
                />
            </label>
            <label className='att-field'>
                <span className='att-label'>
                    {'Plan for tomorrow'}
                    <Counter
                        value={tomorrow}
                        max={form.max_text}
                    />
                </span>
                <textarea
                    className='att-input'
                    value={tomorrow}
                    onChange={(e) => setTomorrow(e.target.value)}
                    maxLength={form.max_text}
                    rows={2}
                />
            </label>

            {error && (
                <div
                    className='att-error'
                    role='alert'
                >{error}</div>
            )}

            <div className='att-eod-foot'>
                <button
                    type='button'
                    className='att-btn'
                    onClick={onCancel}
                >{'Cancel'}</button>
                <button
                    type='submit'
                    className='att-btn att-btn--primary'
                    disabled={sending}
                >{submitLabel}</button>
            </div>
        </form>
    );
}
