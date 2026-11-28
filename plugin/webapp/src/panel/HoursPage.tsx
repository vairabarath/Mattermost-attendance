// The Hours page: today / this week / this month, per day with office/WFH,
// against expected hours, and the split across organizations.

import {fetchHours} from 'api';
import type {Hours, HoursRange} from 'api';
import {hm} from 'format';
import React, {useCallback, useEffect, useState} from 'react';

import {useRefresh} from './refresh';

const RANGES: Array<{key: HoursRange; label: string}> = [
    {key: 'today', label: 'Today'},
    {key: 'week', label: 'Week'},
    {key: 'month', label: 'Month'},
];

const MODE_ICON: Record<string, string> = {office: '🏢', wfh: '🏠'};

export default function HoursPage({teamId}: {teamId: string}) {
    const [range, setRange] = useState<HoursRange>('week');
    const [data, setData] = useState<Hours | null>(null);
    const [failed, setFailed] = useState('');

    const load = useCallback(async () => {
        try {
            setData(await fetchHours(teamId, range));
            setFailed('');
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [teamId, range]);

    useEffect(() => {
        setData(null);
        load();
    }, [load]);
    useRefresh(['session_changed'], load);

    let body: React.ReactNode = <div className='att-empty'>{failed || 'Loading…'}</div>;
    if (data && !failed) {
        const scale = Math.max(data.per_day_hours * 3600, ...data.days.map((d) => d.seconds), 1);
        const pct = data.has_expected ? Math.round((data.worked_until_yesterday / Math.max(1, data.expected_seconds)) * 100) : 0;
        body = (
            <>
                <div className='att-hours-total'>
                    <div>
                        <div className='att-big'>{hm(data.team_seconds)}</div>
                        <div className='att-muted att-small'>{`${data.label} · this organization`}</div>
                    </div>
                    {data.all_seconds !== data.team_seconds && (
                        <div>
                            <div className='att-big att-big--muted'>{hm(data.all_seconds)}</div>
                            <div className='att-muted att-small'>{'all organizations'}</div>
                        </div>
                    )}
                </div>

                {data.has_expected && (
                    <div
                        className='att-expected'
                        aria-label='Against expected hours'
                    >
                        <div className='att-row att-small'>
                            <span>{`Up to ${data.expected_until}: ${hm(data.worked_until_yesterday)} of ${hm(data.expected_seconds)} expected`}</span>
                            <strong className={pct < 90 ? 'att-warn-text' : ''}>{`${pct}%`}</strong>
                        </div>
                        <div
                            className='att-progress'
                            role='progressbar'
                            aria-valuenow={pct}
                            aria-valuemin={0}
                            aria-valuemax={100}
                        >
                            <span
                                className={pct < 90 ? 'att-progress-bar att-progress-bar--warn' : 'att-progress-bar'}
                                style={{width: `${Math.min(100, pct)}%`}}
                            />
                        </div>
                        <div className='att-muted att-small'>{`${data.per_day_hours}h a day, all organizations, holidays excluded. Today counts from tomorrow.`}</div>
                    </div>
                )}

                {data.days.length === 0 ? (
                    <div className='att-empty'>{'No time recorded.'}</div>
                ) : (
                    <ul
                        className='att-days'
                        aria-label='Per day'
                    >
                        {data.days.map((d) => (
                            <li
                                key={d.day}
                                className='att-day'
                            >
                                <span className='att-day-label'>{d.label}</span>
                                <span className='att-day-bar'>
                                    <span style={{width: `${Math.round((d.seconds / scale) * 100)}%`}}/>
                                </span>
                                <span className='att-day-time'>{d.seconds ? hm(d.seconds) : '–'}</span>
                                <span className='att-day-modes'>
                                    {d.modes.map((m) => MODE_ICON[m] ?? '').join(' ')}
                                    {d.open && <span className='att-live'>{' ● now'}</span>}
                                </span>
                            </li>
                        ))}
                    </ul>
                )}

                {data.by_org.length > 1 && (
                    <div className='att-fieldset'>
                        <div className='att-group-h'>{'By organization'}</div>
                        <ul className='att-plain-list'>
                            {data.by_org.map((o) => (
                                <li
                                    key={o.team.id || o.team.name}
                                    className='att-row'
                                >
                                    <span>{o.team.name}</span>
                                    <strong>{hm(o.seconds)}</strong>
                                </li>
                            ))}
                        </ul>
                    </div>
                )}
            </>
        );
    }

    return (
        <div className='att-page-body'>
            <div
                className='att-seg att-seg--tabs'
                role='tablist'
                aria-label='Range'
            >
                {RANGES.map((r) => (
                    <button
                        key={r.key}
                        role='tab'
                        aria-selected={range === r.key}
                        className={`att-seg-btn${range === r.key ? ' is-on' : ''}`}
                        onClick={() => setRange(r.key)}
                    >{r.label}</button>
                ))}
            </div>
            {body}
        </div>
    );
}
