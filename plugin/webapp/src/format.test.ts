import {clock, hm} from 'format';

test('clock', () => {
    expect(clock(0)).toBe('0:00:00');
    expect(clock(3725)).toBe('1:02:05');
    expect(clock(36000 + 59.9)).toBe('10:00:59');
    expect(clock(-5)).toBe('0:00:00');
});

test('hm', () => {
    expect(hm(0)).toBe('0h 0m');
    expect(hm(3725)).toBe('1h 2m');
    expect(hm(59)).toBe('0h 0m');
});
