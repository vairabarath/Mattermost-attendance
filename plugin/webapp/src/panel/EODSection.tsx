// The end-of-day report on the panel: a summary card (not sent / sent, GitHub
// sync) and, when opened, the form in place of the rest of the panel.

import {fetchEOD} from 'api';
import type {EODForm as Form} from 'api';
import {timeOfDay} from 'format';
import React, {useCallback, useEffect, useState} from 'react';

import EODForm from './EODForm';
import {REFRESH_EVENT} from './refresh';

type Props = {
    teamId: string;
    open: boolean;
    onOpen: () => void;
    onClose: () => void;
};

export default function EODSection({teamId, open, onOpen, onClose}: Props) {
    const [form, setForm] = useState<Form | null>(null);
    const [failed, setFailed] = useState('');
    const [notice, setNotice] = useState('');

    const load = useCallback(async () => {
        try {
            setForm(await fetchEOD(teamId));
            setFailed('');
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [teamId]);

    useEffect(() => {
        setForm(null);
        setNotice('');
        load();
    }, [load]);

    // Opening (e.g. right after checking out) reloads: the day and tasks may have changed.
    useEffect(() => {
        if (open) {
            setNotice('');
            load();
        }
    }, [open, load]);

    useEffect(() => {
        // Never reload under an open form: it would wipe what's being typed.
        const refresh = () => !open && load();
        window.addEventListener(REFRESH_EVENT, refresh);
        return () => window.removeEventListener(REFRESH_EVENT, refresh);
    }, [load, open]);

    if (open) {
        if (!form) {
            return <div className='att-empty'>{failed || 'Loading the form…'}</div>;
        }
        if (!form.available) {
            return (
                <section className='att-card att-card--plain'>
                    <div className='att-empty'>{form.message}</div>
                    <button
                        className='att-btn'
                        onClick={onClose}
                    >{'Back'}</button>
                </section>
            );
        }
        return (
            <EODForm
                key={form.state}
                teamId={teamId}
                form={form}
                onCancel={onClose}
                onDone={(saved) => {
                    setForm(saved);
                    setNotice(saved.sync_line?.includes('syncing') ? 'Report sent. Task updates are being written to GitHub.' : 'Report sent.');
                    onClose();
                }}
            />
        );
    }

    let status: React.ReactNode;
    let action = '';
    if (failed) {
        status = <span className='att-muted'>{'Couldn’t load the report status.'}</span>;
    } else if (!form) {
        status = <span className='att-muted'>{'Loading…'}</span>;
    } else if (!form.available) {
        status = <span className='att-muted'>{form.message?.includes('closed') ? form.message : 'Opens when you check out.'}</span>;
    } else if (form.submitted) {
        status = (
            <>
                <span className='att-pill att-pill--progress'>{'Sent'}</span>
                <span className='att-muted'>{` ${form.submitted_at ? timeOfDay(form.submitted_at) : ''} · ${form.sync_line ?? ''}`}</span>
            </>
        );
        action = 'Edit report';
    } else {
        status = <span className='att-pill att-pill--overdue'>{'Not sent yet'}</span>;
        action = 'Write report';
    }

    return (
        <section
            className='att-card att-card--plain'
            aria-label='End-of-day report'
        >
            <div className='att-row'>
                <h3 className='att-h'>{form?.available ? `End-of-day report · ${form.day_label}` : 'End-of-day report'}</h3>
                {action && (
                    <button
                        className='att-btn att-btn--small'
                        onClick={onOpen}
                    >{action}</button>
                )}
            </div>
            <div className='att-eod-status'>{status}</div>
            {notice && (
                <div
                    className='att-notice'
                    role='status'
                >{`✅ ${notice}`}</div>
            )}
        </section>
    );
}
