(function(){
  function initResize(tableId){
    var table=document.getElementById(tableId);if(!table)return;
    var cols=table.querySelectorAll('colgroup col');
    var saved=localStorage.getItem('colw_'+tableId);
    if(saved){try{var w=JSON.parse(saved);w.forEach(function(v,i){if(cols[i])cols[i].style.width=v+'px';});}catch(e){}}
    var handles=table.querySelectorAll('.resize-handle');
    var dragCol=null,startX=0,startW=0;
    handles.forEach(function(h,i){
      h.addEventListener('mousedown',function(e){
        e.preventDefault();dragCol=i;startX=e.clientX;
        startW=cols[i]?(cols[i].offsetWidth||80):80;
        document.addEventListener('mousemove',onMove);
        document.addEventListener('mouseup',onUp);
      });
    });
    function onMove(e){
      if(dragCol===null)return;
      var nw=Math.max(30,startW+(e.clientX-startX));
      if(cols[dragCol])cols[dragCol].style.width=nw+'px';
    }
    function onUp(){
      if(dragCol!==null){
        var w=[];cols.forEach(function(c){w.push(c.offsetWidth||0);});
        localStorage.setItem('colw_'+tableId,JSON.stringify(w));
      }
      dragCol=null;document.removeEventListener('mousemove',onMove);document.removeEventListener('mouseup',onUp);
    }
  }
  initResize('tbl-pos');initResize('tbl-bal');
  (function(){
    var vd=document.getElementById('vdivider');if(!vd)return;
    var left=vd.previousElementSibling,right=vd.nextElementSibling;
    var main=vd.parentElement,saved=localStorage.getItem('panelSplit');
    if(saved){left.style.flex='0 0 '+saved+'%';}
    var dragging=false,startX=0,startW=0;
    vd.addEventListener('mousedown',function(e){
      e.preventDefault();dragging=true;startX=e.clientX;
      startW=left.getBoundingClientRect().width;
      document.addEventListener('mousemove',onM);document.addEventListener('mouseup',onU);
    });
    function onM(e){
      if(!dragging)return;
      var mw=main.getBoundingClientRect().width;
      var nw=Math.min(85,Math.max(15,(startW+(e.clientX-startX))/mw*100));
      left.style.flex='0 0 '+nw+'%';
    }
    function onU(){
      if(dragging){
        var mw=main.getBoundingClientRect().width;
        var lw=left.getBoundingClientRect().width;
        localStorage.setItem('panelSplit',(lw/mw*100).toFixed(2));
      }
      dragging=false;document.removeEventListener('mousemove',onM);document.removeEventListener('mouseup',onU);
    }
  })();
  (function(){
    var sidebar=document.getElementById('sidebar');
    var items=sidebar.querySelectorAll('.sidebar-item');
    var views={
      dashboard:document.getElementById('view-dashboard'),
      statistics:document.getElementById('view-statistics'),
      system:document.getElementById('view-system')
    };
    function switchView(name){
      items.forEach(function(el){el.classList.toggle('active',el.getAttribute('data-view')===name);});
      Object.keys(views).forEach(function(k){
        var v=views[k];
        if(k===name){v.style.display='flex';}else{v.style.display='none';}
      });
      localStorage.setItem('sidebarView',name);
    }
    items.forEach(function(el){
      el.addEventListener('click',function(){switchView(el.getAttribute('data-view'));sidebar.classList.remove('open');});
    });
    var savedView=localStorage.getItem('sidebarView');
    if(savedView&&views[savedView]){switchView(savedView);}
    document.querySelector('.brand').addEventListener('click',function(e){e.stopPropagation();sidebar.classList.toggle('open');});
    document.addEventListener('click',function(e){if(!sidebar.contains(e.target))sidebar.classList.remove('open');});
  })();
})();

Chart.defaults.color='#94a3b8';
Chart.defaults.borderColor='#1e293b';
Chart.defaults.font.family="'JetBrains Mono','ui-monospace',monospace";

var cd=typeof _cd!=='undefined'?_cd:null;

function initCharts(){
  if(!cd)return;

  if(cd.pnlLabels&&cd.pnlLabels.length){
    new Chart(document.getElementById('chart-timeline'),{
      type:'line',
      data:{labels:cd.pnlLabels,datasets:[{label:'Realized P&L',data:cd.pnlData,borderColor:'#3b82f6',backgroundColor:'rgba(59,130,246,0.1)',fill:true,tension:0.3,pointRadius:3,pointBackgroundColor:'#3b82f6'}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{x:{grid:{display:false}},y:{grid:{color:'#1e293b'}}}}
    });
  }

  if(cd.trLabels&&cd.trLabels.length){
    new Chart(document.getElementById('chart-traders'),{
      type:'bar',
      data:{labels:cd.trLabels,datasets:[{label:'P&L',data:cd.trPnL,backgroundColor:cd.trPnL.map(function(v){return v>=0?'#22c55e':'#ef4444';}),borderRadius:3}]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{x:{grid:{color:'#1e293b'}},y:{grid:{display:false}}}}
    });
  }

  if(cd.wlData&&cd.wlData.length){
    new Chart(document.getElementById('chart-winloss'),{
      type:'doughnut',
      data:{labels:['Wins','Losses'],datasets:[{data:cd.wlData,backgroundColor:['#22c55e','#ef4444'],borderWidth:0}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{position:'bottom',labels:{padding:12,usePointStyle:true}}}}
    });
  }

  if(cd.prLabels&&cd.prLabels.length){
    new Chart(document.getElementById('chart-pricerange'),{
      type:'bar',
      data:{labels:cd.prLabels,datasets:[{label:'P&L',data:cd.prPnL,backgroundColor:cd.prPnL.map(function(v){return v>=0?'#22c55e':'#ef4444';}),borderRadius:3}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{x:{grid:{display:false}},y:{grid:{color:'#1e293b'}}}}
    });
  }

  if(cd.dowLabels&&cd.dowLabels.length){
    new Chart(document.getElementById('chart-dow'),{
      type:'bar',
      data:{labels:cd.dowLabels,datasets:[{label:'P&L',data:cd.dowPnL,backgroundColor:'#3b82f6',borderRadius:3}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{x:{grid:{display:false}},y:{grid:{color:'#1e293b'}}}}
    });
  }

  if(cd.hourLabels&&cd.hourLabels.length){
    new Chart(document.getElementById('chart-hourly'),{
      type:'bar',
      data:{labels:cd.hourLabels,datasets:[{label:'P&L',data:cd.hourPnL,backgroundColor:cd.hourPnL.map(function(v){return v>=0?'#22c55e':'#ef4444';}),borderRadius:3}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{x:{grid:{display:false}},y:{grid:{color:'#1e293b'}}}}
    });
  }
}

setTimeout(initCharts,100);
