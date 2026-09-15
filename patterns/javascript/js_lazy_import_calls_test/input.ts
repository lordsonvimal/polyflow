someCall(() => import('./a.js'), 'aExport');
otherCall('bExport', () => import('./b.js'));
