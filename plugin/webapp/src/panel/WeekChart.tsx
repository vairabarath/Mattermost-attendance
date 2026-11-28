// WeekChart: hours per day, Monday to Sunday, against the expected hours a
// day (dashed line). One series in the theme's accent colour, so no legend;
// the list under it on the Hours page is the table view. Each day can be
// hovered or focused (Tab / arrow keys) for its exact time.

import type {Hours, HoursDay} from 'api';
import {hm} from 'format';
import React, {useRef, useState} from 'react';

const MODE: Record<string, string> = {office: 'office', wfh: 'from home'};

// describe is a day's tooltip and screen-reader text.
export function describe(d: HoursDay): string {
    const parts = [d.label];
    if (d.future) {
        parts.push(d.holiday ? `holiday: ${d.holiday}` : 'later this week');
        return parts.join(' · ');
    }
    parts.push(d.seconds ? hm(d.seconds) : 'no time');
    if (d.modes.length) {
        parts.push(d.modes.map((m) => MODE[m] ?? m).join(' + '));
    }
    if (d.open) {
        parts.push('still checked in');
    }
    if (d.holiday) {
        parts.push(`holiday: ${d.holiday}`);
    } else if (!d.workday) {
        parts.push('day off');
    }
    return parts.join(' · ');
}

// niceMax rounds the scale up to a whole number of hours, at least a bit
// above the expected line so it never sits on the top edge.
export function niceMax(days: HoursDay[], perDayHours: number): number {
    const top = Math.max(...days.map((d) => d.seconds), perDayHours * 3600 * 1.15, 3600);
    return Math.ceil(top / 3600) * 3600;
}

export default function WeekChart({data}: {data: Hours}) {
    const [active, setActive] = useState<number | null>(null);
    const cols = useRef<Array<HTMLDivElement | null>>([]);
    const days = data.days;
    if (days.length === 0) {
        return null;
    }
    const max = niceMax(days, data.per_day_hours);
    const pct = (s: number) => `${(s / max) * 100}%`;
    const expected = data.per_day_hours * 3600;
    const mid = Math.round(max / 2 / 3600) * 3600;

    // One gridline mid-way, unless it would crowd the expected line's label.
    const showMid = !data.has_expected || Math.abs(mid - expected) > max / 8;

    const onKey = (i: number) => (e: React.KeyboardEvent) => {
        let next = i;
        if (e.key === 'ArrowRight') {
            next = Math.min(days.length - 1, i + 1);
        } else if (e.key === 'ArrowLeft') {
            next = Math.max(0, i - 1);
        } else if (e.key === 'Escape') {
            setActive(null);
            return;
        } else {
            return;
        }
        e.preventDefault();
        cols.current[next]?.focus();
    };

    return (
        <figure
            className='att-chart'
            aria-label={`Hours per day, ${data.label}`}
        >
            <div
                className='att-chart-plot'
                onMouseLeave={() => setActive(null)}
            >
                {showMid && (
                    <div
                        className='att-chart-grid'
                        style={{bottom: pct(mid)}}
                        aria-hidden='true'
                    >
                        <span>{`${mid / 3600}h`}</span>
                    </div>
                )}
                {data.has_expected && (
                    <div
                        className='att-chart-expected'
                        style={{bottom: pct(expected)}}
                        aria-hidden='true'
                    >
                        <span>{`${data.per_day_hours}h`}</span>
                    </div>
                )}
                <div
                    className='att-chart-cols'
                    role='list'
                >
                    {days.map((d, i) => (
                        <div
                            key={d.day}
                            ref={(el) => {
                                cols.current[i] = el;
                            }}
                            role='listitem'
                            tabIndex={0}
                            aria-label={describe(d)}
                            className={[
                                'att-chart-col',
                                d.workday ? '' : 'is-off',
                                d.future ? 'is-future' : '',
                                active === i ? 'is-active' : '',
                            ].filter(Boolean).join(' ')}
                            onMouseEnter={() => setActive(i)}
                            onFocus={() => setActive(i)}
                            onBlur={() => setActive((a) => (a === i ? null : a))}
                            onKeyDown={onKey(i)}
                        >
                            {d.seconds > 0 && (
                                <span
                                    className={`att-chart-bar${d.open ? ' is-open' : ''}`}
                                    style={{height: pct(d.seconds)}}
                                />
                            )}
                            {active === i && (
                                <span
                                    className={`att-chart-tip${i >= days.length - 2 ? ' is-right' : ''}${i <= 1 ? ' is-left' : ''}`}
                                    role='tooltip'
                                >{describe(d)}</span>
                            )}
                        </div>
                    ))}
                </div>
            </div>
            <div
                className='att-chart-axis'
                aria-hidden='true'
            >
                {days.map((d) => (
                    <span
                        key={d.day}
                        className={d.workday ? '' : 'is-off'}
                    >{d.label.split(' ')[0].slice(0, 2)}</span>
                ))}
            </div>
            {data.has_expected && (
                <figcaption className='att-chart-key'>
                    <span
                        className='att-chart-key-line'
                        aria-hidden='true'
                    />
                    {`${data.per_day_hours}h expected a day`}
                </figcaption>
            )}
        </figure>
    );
}
