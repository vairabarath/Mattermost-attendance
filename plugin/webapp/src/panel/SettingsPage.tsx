// My settings: the GitHub link, the member's attendance settings (view
// only — admins change them) and a connection check.

import {fetchSettings} from 'api';
import type {Settings} from 'api';
import {fetchHealth} from 'client';
import type {Health} from 'client';
import React, {useCallback, useEffect, useState} from 'react';

function Row({label, children}: {label: string; children: React.ReactNode}) {
    return (
        <div className='att-kv'>
            <span className='att-muted'>{label}</span>
            <span>{children}</span>
        </div>
    );
}

function Check({ok, children}: {ok: boolean | null; children: React.ReactNode}) {
    let mark = '…';
    if (ok !== null) {
        mark = ok ? '✅' : '⚠️';
    }
    return (
        <li className='att-check-row'>
            <span aria-hidden='true'>{mark}</span>
            <span>{children}</span>
        </li>
    );
}

export default function SettingsPage({teamId}: {teamId: string}) {
    const [s, setS] = useState<Settings | null>(null);
    const [failed, setFailed] = useState('');
    const [health, setHealth] = useState<Health | null>(null);
    const [healthErr, setHealthErr] = useState('');

    const load = useCallback(async () => {
        setFailed('');
        setHealthErr('');
        fetchHealth().then(setHealth).catch((e: Error) => setHealthErr(e.message));
        try {
            setS(await fetchSettings(teamId));
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [teamId]);

    useEffect(() => {
        setS(null);
        setHealth(null);
        load();
    }, [load]);

    let botOk: boolean | null = null;
    if (s) {
        botOk = true;
    } else if (failed) {
        botOk = false;
    }
    let pluginOk: boolean | null = null;
    if (health) {
        pluginOk = health.ok;
    } else if (healthErr) {
        pluginOk = false;
    }

    return (
        <div className='att-page-body'>
            <section
                className='att-card att-card--plain'
                aria-label='GitHub'
            >
                <h3 className='att-h'>{'GitHub'}</h3>
                {!s && <div className='att-empty'>{failed || 'Loading…'}</div>}
                {s && !s.github_set_up && <div className='att-empty'>{`${s.team.name} doesn’t use GitHub tasks.`}</div>}
                {s?.github_set_up && s.github_login && (
                    <>
                        <Row label='Linked account'>
                            {/* Mattermost's ExternalLink component isn't available to plugins; rel=noopener noreferrer keeps the new tab safe. */}
                            {/* eslint-disable-next-line @mattermost/use-external-link */}
                            <a
                                href={`https://github.com/${encodeURIComponent(s.github_login)}`}
                                target='_blank'
                                rel='noopener noreferrer'
                            >{`✅ ${s.github_login}`}</a>
                        </Row>
                        <Row label='Organization'>{s.github_org}</Row>
                        <div className='att-muted att-small'>{'An admin linked this account after checking it’s a member of the organization. To change it, ask an admin.'}</div>
                    </>
                )}
                {s?.github_set_up && !s.github_login && (
                    <div className='att-empty'>
                        {'⚠️ Not linked yet. Send your GitHub username to an admin of this team; they run '}
                        <code>{'/att member @you github <username>'}</code>{'.'}
                    </div>
                )}
            </section>

            <section
                className='att-card att-card--plain'
                aria-label='My attendance settings'
            >
                <h3 className='att-h'>{'My attendance settings'}</h3>
                {s && (
                    <>
                        <Row label='Team'>{s.team.name}</Row>
                        <Row label='Expected hours a day'>{`${s.expected_hours}h (all organizations)`}</Row>
                        <Row label='Counted in reports'>{s.tracked ? 'Yes' : 'No'}</Row>
                        <Row label='10:30 reminder'>{s.reminders_on ? 'On' : 'Off'}</Row>
                        <div className='att-muted att-small'>{'Admins change these with /att member.'}</div>
                    </>
                )}
            </section>

            <section
                className='att-card att-card--plain'
                aria-label='Connection'
            >
                <div className='att-row'>
                    <h3 className='att-h'>{'Connection'}</h3>
                    <button
                        className='att-btn att-btn--small'
                        onClick={load}
                    >{'Check again'}</button>
                </div>
                <ul className='att-plain-list'>
                    <Check ok={pluginOk}>{pluginOk === false ? `Plugin: ${health?.error || healthErr}` : `Plugin${health ? ` ${health.version}` : ''}`}</Check>
                    <Check ok={botOk}>{botOk === false ? `Attendance service: ${failed}` : 'Attendance service'}</Check>
                    {s?.github_set_up && <Check ok={true}>{`GitHub project for ${s.team.name}`}</Check>}
                </ul>
            </section>
        </div>
    );
}
