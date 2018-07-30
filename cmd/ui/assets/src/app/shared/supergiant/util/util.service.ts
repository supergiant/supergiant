import { Injectable } from '@angular/core';
import { Http, Response, Headers } from '@angular/http';
import { HttpClient } from '@angular/common/http';
import { Observable } from 'rxjs/Observable';
import { Location } from '@angular/common';

@Injectable()
export class UtilService {
  serverEndpoint = 'http://localhost:3000';
  sessionToken: string;
  SessionID: string;

  constructor(
    private http: HttpClient,
    private location: Location,
  ) {
    if (window.location.pathname.split('/')[2] === 'ui') {
      this.serverEndpoint = '/' + window.location.pathname.split('/')[1] + '/server';
    } else {
      if (window.location.hostname === 'localhost') {
        this.serverEndpoint = window.location.protocol + '//' + window.location.hostname + ':3000';
      } else {
        this.serverEndpoint = window.location.protocol + '//' + window.location.hostname;
      }
    }
  }

  fetch(path) {
    return this.http.get(this.serverEndpoint + path + '?limit=1000', { observe: "response" });
  }

  fetchNoMap(path) {
    return this.http.get(this.serverEndpoint + path, { observe: "response" });
  }

  post(path, data) {
    const json = JSON.stringify(data);
    return this.http.post(this.serverEndpoint + path, json, { observe: "response" });
  }

  update(path, data) {
    const json = JSON.stringify(data);
    return this.http.put(this.serverEndpoint + path, json, { observe: "response" });
  }

  destroy(path) {
    return this.http.delete(this.serverEndpoint + path, { observe: "response" });
  }
}
