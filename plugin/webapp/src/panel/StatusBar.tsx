// Home's status bar: where you are and the one action you need most —
// Check in, Back (on a break) or Check out (with a confirmation).

import {clock} from 'format';
import React, {useState} from 'react';

import {useNow} from './hooks';
import {liveSeconds, sessionState} from './session';
import type {SessionProps} from './session';

export default function StatusBar(p: SessionProps) {
    const {me, busy, run} = p;
    const [confirm, setConfirm] = useState(false);
    const session = me.session;
    const now = useNow(Boolean(session));
    const t = liveSeconds(p, now);
    const state = sessionState(me);

    let label = 'Not checked in';
    if (session?.on_break) {
        label = 'On break';
    } else if (session) {
        label = 'Working';
    }

    let action: React.ReactNode;
    if (!session) {
        action = (
            <button
                className='att-btn att-btn--primary att-btn--small'
                disabled={Boolean(busy)}
                onClick={() => run('in')}
            >{busy === 'in' ? '…' : '▶ Check in'}</button>
        );
    } else if (session.on_break) {
        action = (
            <button
                className='att-btn att-btn--primary att-btn--small'
                disabled={Boolean(busy)}
                onClick={() => run('back')}
            >{busy === 'back' ? '…' : '▶ Back'}</button>
        );
    } else if (confirm) {
        action = (
            <span className='att-confirm'>
                <button
                    className='att-btn att-btn--danger att-btn--small'
                    disabled={Boolean(busy)}
                    onClick={() => {
                        setConfirm(false);
                        run('out');
                    }}
                >{busy === 'out' ? '…' : 'Yes, check out'}</button>
                <button
                    className='att-link'
                    onClick={() => setConfirm(false)}
                >{'No'}</button>
            </span>
        );
    } else {
        action = (
            <button
                className='att-btn att-btn--danger att-btn--small'
                disabled={Boolean(busy)}
                onClick={() => setConfirm(true)}
            >{'Check out'}</button>
        );
    }

    return (
        <section
            className={`att-bar att-bar--${state}`}
            aria-label='Status'
        >
            <span className={`att-dot att-dot--${state}`}/>
            <span className='att-bar-label'>
                {label}
                {session && <span className='att-bar-time'>{` · ${clock(t.work)}`}</span>}
                {t.elsewhere && session && <span className='att-muted'>{` · ${session.team.name}`}</span>}
            </span>
            {action}
        </section>
    );
}
