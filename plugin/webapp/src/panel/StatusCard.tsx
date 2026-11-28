// The Attendance page's status card: live timer, check in (with a note) /
// break / back / check out (with a confirmation), and today's totals.

import {clock, hm, timeOfDay} from 'format';
import React, {useState} from 'react';

import {useNow} from './hooks';
import {liveSeconds, sessionState} from './session';
import type {SessionProps} from './session';

export default function StatusCard(p: SessionProps) {
    const {me, busy, run} = p;
    const [confirmOut, setConfirmOut] = useState(false);
    const [showNote, setShowNote] = useState(false);
    const [note, setNote] = useState('');
    const session = me.session;
    const now = useNow(Boolean(session));
    const t = liveSeconds(p, now);
    const state = sessionState(me);
    const modeLabel = session?.mode === 'wfh' ? '🏠 Working from home' : '🏢 Office';

    const go = (action: 'in' | 'break' | 'back' | 'out', text = '') => {
        setConfirmOut(false);
        run(action, text);
        if (action === 'in') {
            setNote('');
            setShowNote(false);
        }
    };

    return (
        <section
            className={`att-card att-card--${state}`}
            aria-label='Attendance status'
        >
            {!session && (
                <>
                    <div className='att-status'>
                        <span className='att-dot att-dot--off'/>
                        {'Not checked in'}
                    </div>
                    {showNote ? (
                        <textarea
                            className='att-input'
                            placeholder='Note (optional), e.g. “standup at 10”'
                            maxLength={500}
                            value={note}
                            onChange={(e) => setNote(e.target.value)}
                            aria-label='Check-in note'
                        />
                    ) : (
                        <button
                            className='att-link'
                            onClick={() => setShowNote(true)}
                        >{'+ Add a note'}</button>
                    )}
                    <button
                        className='att-btn att-btn--primary att-btn--wide'
                        disabled={Boolean(busy)}
                        onClick={() => go('in', note)}
                    >{busy === 'in' ? 'Checking in…' : '▶ Check in'}</button>
                </>
            )}

            {session && (
                <>
                    <div className='att-status'>
                        <span className={`att-dot att-dot--${state}`}/>
                        {session.on_break ? 'On break' : 'Working'}
                        {t.elsewhere && <span className='att-muted'>{` · in ${session.team.name}`}</span>}
                    </div>
                    <div
                        className='att-timer'
                        aria-label='Time worked this session'
                    >{clock(t.work)}</div>
                    <div className='att-muted'>
                        {`since ${timeOfDay(session.check_in)} · ${modeLabel}`}
                        {session.on_break && ` · break ${hm(t.breakSeconds)}`}
                    </div>
                    <div className='att-actions'>
                        {session.on_break ? (
                            <button
                                className='att-btn att-btn--primary'
                                disabled={Boolean(busy)}
                                onClick={() => go('back')}
                            >{busy === 'back' ? '…' : '▶ Back'}</button>
                        ) : (
                            <button
                                className='att-btn'
                                disabled={Boolean(busy)}
                                onClick={() => go('break')}
                            >{busy === 'break' ? '…' : '☕ Break'}</button>
                        )}
                        {confirmOut ? (
                            <span className='att-confirm'>
                                <button
                                    className='att-btn att-btn--danger'
                                    disabled={Boolean(busy)}
                                    onClick={() => go('out')}
                                >{busy === 'out' ? 'Checking out…' : 'Yes, check out'}</button>
                                <button
                                    className='att-link'
                                    onClick={() => setConfirmOut(false)}
                                >{'Cancel'}</button>
                            </span>
                        ) : (
                            <button
                                className='att-btn att-btn--danger'
                                disabled={Boolean(busy)}
                                onClick={() => setConfirmOut(true)}
                            >{'🔴 Check out'}</button>
                        )}
                    </div>
                </>
            )}
            <div
                className='att-card-foot'
                aria-label='Today'
            >
                <span>
                    <span className='att-muted'>{'Today '}</span>
                    <strong>{hm(t.today)}</strong>
                </span>
                {Math.floor(t.all / 60) !== Math.floor(t.today / 60) && (
                    <span>
                        <span className='att-muted'>{'All orgs '}</span>
                        <strong>{hm(t.all)}</strong>
                    </span>
                )}
            </div>
        </section>
    );
}
