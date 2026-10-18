import { downloadBlob } from '@/utils/download';

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error('Failed to render chart image'));
    image.src = url;
  });
}

export async function exportChartPng(
  svg: SVGSVGElement,
  filename = 'xrouter-usage.png'
): Promise<void> {
  const clone = svg.cloneNode(true) as SVGSVGElement;
  const viewBox = svg.viewBox.baseVal;
  const width = Math.max(1, Math.round(viewBox.width || svg.clientWidth || 900));
  const height = Math.max(1, Math.round(viewBox.height || svg.clientHeight || 220));
  clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
  clone.setAttribute('width', String(width));
  clone.setAttribute('height', String(height));

  clone.querySelectorAll('line').forEach((line) => {
    line.setAttribute('stroke', '#e5e7eb');
    line.setAttribute('stroke-width', '1');
  });
  clone.querySelectorAll('polyline').forEach((line, index) => {
    if (index === 0) {
      line.setAttribute('fill', 'none');
      line.setAttribute('stroke', '#3b82f6');
      line.setAttribute('stroke-width', '2.5');
    } else {
      line.setAttribute('fill', 'none');
      line.setAttribute('stroke', '#22c55e');
      line.setAttribute('stroke-width', '1.5');
      line.setAttribute('stroke-dasharray', '5 4');
    }
  });
  clone.querySelectorAll('text').forEach((text) => {
    text.setAttribute('fill', '#6b7280');
    text.setAttribute('font-size', '11');
    text.setAttribute('font-family', 'Arial, sans-serif');
  });
  clone.querySelectorAll('path').forEach((path) => {
    path.setAttribute('fill', '#3b82f6');
    path.setAttribute('fill-opacity', '0.15');
  });

  const xml = new XMLSerializer().serializeToString(clone);
  const svgBlob = new Blob([xml], { type: 'image/svg+xml;charset=utf-8' });
  const url = URL.createObjectURL(svgBlob);
  try {
    const image = await loadImage(url);
    const scale = 2;
    const canvas = document.createElement('canvas');
    canvas.width = width * scale;
    canvas.height = height * scale;
    const context = canvas.getContext('2d');
    if (!context) {
      throw new Error('Canvas is unavailable');
    }
    context.fillStyle = '#ffffff';
    context.fillRect(0, 0, canvas.width, canvas.height);
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    const blob = await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((value) => {
        if (value) {
          resolve(value);
        } else {
          reject(new Error('Failed to encode PNG'));
        }
      }, 'image/png');
    });
    downloadBlob({ filename, blob });
  } finally {
    URL.revokeObjectURL(url);
  }
}
