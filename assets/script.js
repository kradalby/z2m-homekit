(function () {
  function formatTime(value) {
    if (!value) {
      return "unknown";
    }
    const date = new Date(value);
    if (isNaN(date)) {
      return value;
    }
    return date.toLocaleTimeString();
  }

  function updateDeviceCard(data) {
    console.log('SSE Data received:', data);
    const card = document.querySelector('[data-device-id="' + data.device_id + '"]');
    if (!card) {
      return;
    }

    // Update On/Off state for lights/outlets
    if (data.on !== undefined && data.on !== null) {
      card.classList.toggle('on', data.on);
      card.classList.toggle('off', !data.on);

      const statusLabel = card.querySelector('[data-role="status-label"]');
      if (statusLabel) {
        statusLabel.textContent = 'Status: ' + (data.on ? 'ON' : 'OFF');
      }

      const actionInput = card.querySelector('[data-role="action-input"]');
      const button = card.querySelector('[data-role="toggle-button"]');
      if (actionInput && button) {
        if (data.on) {
          actionInput.value = 'off';
          button.textContent = 'Turn Off';
          button.classList.remove('on');
          button.classList.add('off');
        } else {
          actionInput.value = 'on';
          button.textContent = 'Turn On';
          button.classList.remove('off');
          button.classList.add('on');
        }
      }
    }

    const lastUpdated = card.querySelector('[data-role="last-updated"]');
    if (lastUpdated) {
      lastUpdated.textContent = 'Last updated: ' + formatTime(data.last_updated);
    }

    const indicator = card.querySelector('[data-role="connection-indicator"]');
    if (indicator) {
      indicator.classList.remove('connected', 'stale', 'disconnected');
      indicator.classList.add(data.connection_state || 'disconnected');
    }

    const connectionText = card.querySelector('[data-role="connection-text"]');
    if (connectionText) {
      connectionText.textContent = data.connection_note || '';
    }

    // Update sensor values
    const tempEl = card.querySelector('[data-role="temperature-value"]');
    if (tempEl && data.temperature !== undefined && data.temperature !== null) {
      tempEl.textContent = data.temperature.toFixed(1) + ' °C';
    }

    const humidityEl = card.querySelector('[data-role="humidity-value"]');
    if (humidityEl && data.humidity !== undefined && data.humidity !== null) {
      humidityEl.textContent = data.humidity.toFixed(1) + ' %';
    }

    const batteryEl = card.querySelector('[data-role="battery-value"]');
    if (batteryEl && data.battery !== undefined && data.battery !== null) {
      batteryEl.textContent = data.battery + ' %';
    }

    const occupancyEl = card.querySelector('[data-role="occupancy-value"]');
    if (occupancyEl && data.occupancy !== undefined && data.occupancy !== null) {
      occupancyEl.textContent = data.occupancy ? 'Detected' : 'Clear';
    }

    // Update light values
    const brightnessEl = card.querySelector('[data-role="brightness-value"]');
    if (brightnessEl && data.brightness !== undefined && data.brightness !== null) {
      brightnessEl.textContent = data.brightness + '%';
    }

    const hueEl = card.querySelector('[data-role="hue-value"]');
    if (hueEl && data.hue !== undefined && data.hue !== null) {
      hueEl.textContent = data.hue.toFixed(0) + '°';
    }

    const satEl = card.querySelector('[data-role="saturation-value"]');
    if (satEl && data.saturation !== undefined && data.saturation !== null) {
      satEl.textContent = data.saturation.toFixed(0) + '%';
    }

    const ctEl = card.querySelector('[data-role="color-temp-value"]');
    if (ctEl && data.color_temp !== undefined && data.color_temp !== null) {
      ctEl.textContent = data.color_temp + ' mireds';
    }
  }

  document.addEventListener('DOMContentLoaded', function () {
    const source = new EventSource('/events');
    source.onmessage = function (event) {
      try {
        const data = JSON.parse(event.data);
        updateDeviceCard(data);
      } catch (err) {
        console.error('invalid SSE payload', err);
      }
    };
  });
})();
